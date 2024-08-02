package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type ClosedPNL struct {
	Symbol       string
	Pnl          float64
	Qty          Decimal
	AvgExitPrice Decimal
}

func isClose(a Decimal, b Decimal, tolerance Decimal) bool {
	diff := a.Sub(b).Abs()
	return diff.LessThanOrEqual(tolerance)
}

func Int64ToIntPtr(i64 *int64) *int {
	if i64 == nil {
		return nil
	}
	i := int(*i64)
	return &i
}

// truncates a float64 number to the specified number of decimal places
// without rounding, and returns it as a string.
func truncate(num float64, dp int) string {
	s := fmt.Sprintf("%.*f", dp+1, num)
	dot := strings.LastIndex(s, ".")
	if dot == -1 || dot+dp+1 > len(s) {
		return s
	}
	return s[:dot+dp]
}

// waits until another go routine isnt getting the pnl for the same symbol, so that
// proxies arent being used redundantly to make requests that we know wont return
// anything useful
func (bot *Bot) _pnlWaitUntilAvailable(symbol string) {
	for {
		bot.PnlWatchingMtx.Lock()

		// check if the symbol is being watched
		exists := false
		for _, sym := range bot.PnlWatching {
			if sym == symbol {
				exists = true
				break
			}
		}
		bot.PnlWatchingMtx.Unlock()

		// return if it is not being watched
		if !exists {
			return
		}

		time.Sleep(1 * time.Second)
	}
}

// Gets the pnl for a closed position. will only ever be able to run once for each
// symbol. Returns a string, already truncated to DP. closedAfter is a timestamp in
// seconds.
func (bot *Bot) getPnl(symbol string, closedAfter int64, qty Decimal) (*ClosedPNL, error) {
	log.Printf(
		"%s : getting pnl for %s %s",
		bot.getHash(), qty, symbol,
	)

	// wait 5s because it wont be available straight away anyway
	time.Sleep(5 * time.Second)

	// wait until no other goroutines are watching it
	bot._pnlWaitUntilAvailable(symbol)

	// add to watching list
	bot.PnlWatchingMtx.Lock()
	bot.PnlWatching = append(bot.PnlWatching, symbol)
	bot.PnlWatchingMtx.Unlock()

	// attempt 5 times with a 5 second wait between each attempt
	var err error
	for i := 1; i <= 10; i++ {
		// get the pnl for this symbol
		var pnls []ClosedPNL
		pnls, err = bot.ExcClient.getClosedPNL(symbol, &closedAfter)

		if err == nil {
			// parse data to get a pnl with qty close enough to requested qty
			pnlStrings := make([]string, len(pnls))

			for ind, pnl := range pnls {
				pnlStrings[ind] = pnl.Qty.String()

				if isClose(pnl.Qty, qty, decimal.NewFromFloat(0.01)) {
					log.Printf(
						"%s : found pnl (%0.2f) for %s %s",
						bot.getHash(), pnl.Pnl, qty, symbol,
					)
					return &pnl, nil
				}
			}

			log.Printf(
				"%s : got pnls: (%s) for %s %s but none are close enough",
				bot.getHash(), strings.Join(pnlStrings, " "), qty, symbol,
			)
		} else {
			log.Printf("%s : err getting pnl, %v", bot.getHash(), err)
		}

		// sleep 5s
		time.Sleep(5 * time.Second)
	}

	// didnt find it in 5 attempts, return err
	if err == nil {
		err = errors.New("timeout")
	}
	return nil, fmt.Errorf("failed to get pnl, %v", err)
}
