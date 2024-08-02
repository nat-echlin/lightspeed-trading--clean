package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	cls "github.com/lightspeed-trading/app/classes"
	"github.com/shopspring/decimal"
)

// Trader holds the data that an individual trader requires.
type Trader struct {
	uid       string
	name      string
	positions []TraderPosition
	posdata   []byte
}

// used to shrink the error string after a request to binance fails
func parseBinErr(err error) error {
	errStr := err.Error()
	if strings.HasSuffix(errStr, "(Client.Timeout exceeded while awaiting headers)") {
		return fmt.Errorf("proxy timeout")

	} else if strings.HasSuffix(errStr, "Proxy Error (The selected relay is offline or busy processing other threads)") {
		return fmt.Errorf("proxy error")

	} else if strings.HasSuffix(errStr, "EOF") {
		return fmt.Errorf("proxy failure")
	}
	return err
}

// get the name of a trader
func (tr *Trader) getName(clients []ClientProxy) error {
	ratelimit := time.Duration(5000/len(clients)) * time.Millisecond

	// attempt 5 times before returning error
	var heldErr error
	for i := 1; i <= 5; i++ {
		// sleep for 1 ratelimit if its not the 1st attempt
		if i != 1 {
			time.Sleep(ratelimit)
		}

		// work out which client to use
		cli := clients[i%len(clients)].Client

		// create then do request
		req, _ := http.NewRequest(
			"POST",
			"https://www.binance.com/bapi/futures/v2/public/future/leaderboard/getOtherLeaderboardBaseInfo",
			bytes.NewBuffer(tr.posdata),
		)
		req.Header.Set("Content-Type", "application/json")
		resp, err := cli.Do(req)

		// parse errors
		if err != nil {
			err = parseBinErr(err)
			heldErr = err
			log.Printf("%s : err requesting name : %v", tr.uid, err)
			continue
		}

		// check status code
		if resp.StatusCode != 200 {
			errorStr := fmt.Sprintf("bad status code, status: %d", resp.StatusCode)
			heldErr = errors.New(errorStr)
			log.Printf("%s : %s", tr.uid, errorStr)
			continue
		}

		// read body
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			heldErr = err
			log.Printf("%s : err reading body: %v", tr.uid, err)
			continue
		} else if resp.ContentLength == 0 {
			err = errors.New("no json receieved")
			heldErr = err
			log.Printf("%s : err reading body: %v", tr.uid, err)
			continue
		}

		// parse json response
		var respJSON struct {
			Data struct {
				NickName string `json:"nickName"`
			} `json:"data"`
		}
		json.Unmarshal(body, &respJSON)

		// set the name of the trader
		if respJSON.Data.NickName == "" {
			log.Printf("%s : appears to have no name, likely bad trader uid", tr.uid)
			heldErr = errors.New("bad trader uid")
			continue
		}

		tr.name = (respJSON.Data.NickName)
		log.Printf("%s : got name : %s", tr.uid, tr.name)
		return nil
	}
	return fmt.Errorf("failed to get name, %v", heldErr)
}

// for a trader tr, request their positions from the binance api
func (tr Trader) getPositions(client *http.Client) ([]TraderPosition, string, error) {
	// create the request
	req, _ := http.NewRequest(
		"POST",
		"https://www.binance.com/bapi/futures/v1/public/future/leaderboard/getOtherPosition",
		bytes.NewBuffer(tr.posdata),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Close = true
	resp, err := client.Do(req)

	// parse out the error if there is one
	if err != nil {
		// 	lenError := len(err.Error())
		// 	if lenError > 116 && err.Error()[91:116] == "context deadline exceeded" {
		// 		err = errors.New("proxy timeout")
		// 	} else if lenError > 102 && err.Error()[91:102] == "Proxy Error" {
		// 		err = errors.New("proxy error")
		// 	}
		// 	return nil, "", err

		return nil, "", parseBinErr(err)
	}

	// check the response status code
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	// read the body
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("failed to read body of response")
		return nil, "", err
	} else if resp.ContentLength == 0 {
		return nil, "", errors.New("no json received")
	}

	// parse the json response body into a go struct (cls.BinanceResp)
	var respJSON cls.BinanceResp

	err = json.Unmarshal(body, &respJSON)
	if err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal json response: %s\n err: %v", string(body), err)
	}

	positions := make([]TraderPosition, len(respJSON.Data.OtherPositionRetList))

	// iterate through each raw pos rp in the binance response, create a Position for each one
	for ind, rp := range respJSON.Data.OtherPositionRetList {
		positions[ind] = cls.NewPosition(rp.Amount, rp.Timestamp, rp.Symbol, rp.EntryPrice, rp.Pnl, rp.MarkPrice)
	}

	return positions, string(body), nil
}

func (tr *Trader) getInitPositions(clients []ClientProxy) error {
	for _, client := range clients {
		positions, _, err := tr.getPositions(client.Client)
		if err != nil {
			log.Printf("%s : err getting positions; %v", tr.name, err)
			continue
		}
		tr.positions = positions
		return nil
	}
	if len(clients) > 0 {
		return fmt.Errorf("%s : all clients failed to access", tr.name)
	} else {
		return fmt.Errorf("%s : no clients were given", tr.name)
	}
}

func newTrader(uid string) Trader {
	data, _ := json.Marshal(map[string]string{
		"encryptedUid": uid,
		"tradeType":    "PERPETUAL",
	})

	tr := Trader{uid, "", []TraderPosition{}, data}
	return tr
}

// given a list of new positions nPs, compare to the current ones and return a
// list of changes that have been made. PNLs in CHANGE updates will only be
// accurate for decreases, and therefore NOT accurate for increases
func (tr *Trader) comparePositions(nPs []TraderPosition) ([]cls.Update, error) {

	updates := []cls.Update{}
	upto := len(nPs) - 1

	for _, old := range tr.positions {
		thisPosIndex := -1

		for i := 0; i <= upto; i++ {
			if old.Symbol == nPs[i].Symbol && old.Side == nPs[i].Side {
				thisPosIndex = i

				if old.Qty == nPs[i].Qty {
					// no change
					break
				}

				// new qty != old qty, so send a CHANGE update
				newQtyAbs := math.Abs(nPs[i].Qty)
				oldQtyAbs := math.Abs(old.Qty)
				pnl := (oldQtyAbs - newQtyAbs) * (old.MarkPrice - old.EntryPrice)

				side := nPs[i].Side
				symbol := nPs[i].Symbol

				updates = append(
					updates,
					cls.Update{
						Type:        cls.ChangeLS,
						Symbol:      symbol,
						Side:        side,
						BIN_qty:     newQtyAbs,
						Timestamp:   nPs[i].Timestamp,
						Old_BIN_qty: oldQtyAbs,
						Entry:       decimal.NewFromFloat(nPs[i].EntryPrice),
						Pnl:         pnl,
						MarkPrice:   nPs[i].MarkPrice,
					},
				)

				log.Printf("%s : change position %s %s", tr.name, side, symbol)
			}
		}
		if thisPosIndex == -1 {
			// old has been closed, so send a CLOSE update
			// we can use the old entry & market because it is accurate
			// to the last time that the monitor was ran for this trader,
			// which should be very low (sub 5s), therefore our
			// calculations will be accurate enough
			pnl := old.Qty * (old.MarkPrice - old.EntryPrice)

			updates = append(
				updates,
				cls.Update{
					Type:        cls.CloseLS,
					Symbol:      old.Symbol,
					Side:        old.Side,
					BIN_qty:     0,
					Timestamp:   old.Timestamp,
					Old_BIN_qty: math.Abs(old.Qty),
					Entry:       decimal.NewFromFloat(old.EntryPrice),
					Pnl:         pnl,
					MarkPrice:   old.MarkPrice,
				},
			)
			log.Printf("%s : close position %s %s", tr.name, old.Side, old.Symbol)

		} else {
			// 'remove' at thisPosIndex
			nPs[thisPosIndex] = nPs[upto]
			upto -= 1
		}
	}

	if upto != -1 {
		// positions have been opened, send an OPEN update
		for i := 0; i <= upto; i++ {
			symbol := nPs[i].Symbol
			side := nPs[i].Side

			updates = append(
				updates,
				cls.Update{
					Type:        cls.OpenLS,
					Symbol:      symbol,
					Side:        side,
					BIN_qty:     math.Abs(nPs[i].Qty),
					Timestamp:   nPs[i].Timestamp,
					Old_BIN_qty: 0,
					Entry:       decimal.NewFromFloat(nPs[i].EntryPrice),
					Pnl:         0,
					MarkPrice:   nPs[i].MarkPrice,
				},
			)
			log.Printf("%s : open position %s %s", tr.name, side, symbol)
		}
	}

	return updates, nil
}

func (tr *Trader) getUpdates(client *http.Client) ([]cls.Update, []TraderPosition, error) {
	nPs, _, err := tr.getPositions(client)

	if err != nil {
		return nil, nil, err
	}

	// create a copy because comparePositions will mutate the old position slice
	nPsCopy := make([]TraderPosition, len(nPs))
	copy(nPsCopy, nPs)

	updates, err := tr.comparePositions(nPsCopy)

	if err != nil {
		return nil, nil, err
	}

	return updates, nPs, nil
}

// func isHedged(newPositions []Position, symbol string) bool {
// 	count := 0

// 	for _, pos := range newPositions {
// 		if pos.symbol == symbol {
// 			count++
// 		}
// 	}

// 	if count == 2 {
// 		return true
// 	} else {
// 		return false
// 	}
// }
