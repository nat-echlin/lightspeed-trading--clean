package main

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"

	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	"github.com/nat-echlin/bybit"
	"github.com/shopspring/decimal"
)

type DispatchResult struct {
	UserHash string
	Errs     []error
}

// given a map of bots, return a slice
func botMapToSlice(botMap map[string]*Bot) []*Bot {
	botSlice := make([]*Bot, len(botMap))
	i := 0
	for _, bot := range botMap {
		botSlice[i] = bot
		i++
	}
	return botSlice
}

// handle panic in dispatch to a single bot
func handleSingleBotPanic(bot *Bot, resultsChan chan<- DispatchResult) {
	if r := recover(); r != nil {
		errMsg := getPanicMsg(r)

		go common.LogAndSendAlertF(errMsg)
		resultsChan <- DispatchResult{cls.GetBotHash(bot.UserID, bot.BotID), []error{errors.New(errMsg)}}
	}
}

// handle panic in trade prep to multiple bots
func handleMultiBotPanic(upd cls.Update, bots []*Bot, doIgnore bool) {
	if r := recover(); r != nil {
		errMsg := getPanicMsg(r)

		go common.LogAndSendAlertF(errMsg)

		err := errors.New(errMsg)
		onFailedTradePrep(upd, bots, doIgnore, err)
	}
}

// get the price of a symbol on multiple exchanges
func getPrices(symbol string, retChan chan<- PricesRes) {
	defer func() {
		if r := recover(); r != nil {
			err := getPanicMsg(r)
			retChan <- PricesRes{Prices: nil, PanicErr: errors.New(err)}
		}
	}()

	numExchanges := 1
	priceResultsChan := make(chan cls.Price, numExchanges)

	// get bybit price, send to results channel
	go func() {
		apiCli := bybit.NewClient()
		bybCli := bybitNormalised{
			cli: apiCli,
		}

		price, isSupported, err := bybCli.getPrice(symbol)
		priceResultsChan <- cls.Price{
			ExcName:     cls.Bybit,
			Market:      price,
			Err:         err,
			IsSupported: isSupported,
		}
	}()

	// get phemex price
	// TODO

	// get data for bots to determine stuff about trigger price
	// TODO

	// collect results from exchanges
	prices := cls.PricesRes{
		Prices:   map[string]cls.Price{},
		PanicErr: nil,
	}
	for i := 1; i <= numExchanges; i += 1 {
		// get res
		res := <-priceResultsChan
		prices.Prices[res.ExcName] = res
	}

	// return back to sender
	retChan <- prices
}

// ran when prep failed for a trade - messages all bots that it failed,
// and ignores the symbol (if specified)
func onFailedTradePrep(upd cls.Update, bots []*Bot, doIgnore bool, err error) {
	fmt.Printf("failed to prep update: %s\nerror:%v", upd, err)

	// build error description
	var tradeDescriptor string
	switch upd.Type {
	case cls.OpenLS:
		tradeDescriptor = "opened"
	case cls.CloseLS:
		tradeDescriptor = "closed"
	default:
		if upd.IsIncrease() {
			tradeDescriptor = "added to"
		} else {
			tradeDescriptor = "sold part of"
		}
	}

	alertDesc := fmt.Sprintf(
		"Failed to place orders after the trader you are copying %s a position in %s %s.",
		tradeDescriptor,
		upd.Side,
		upd.Symbol,
	)

	// loop over all given bots
	for _, bot := range bots {
		go func(bot *Bot) {
			// ignore if specifeied
			if doIgnore {
				bot.ignore(upd.Symbol)
			}

			// send notif
			config, err := bot.getConfig()
			if err != nil {
				common.SendStaffAlert("Failed to send error alerts to users after trade prep failed.")
				log.Printf("failed to get config after failed trade prep, %v", err)
				return
			}
			bot.sendErrorNotif(alertDesc, config)
		}(bot)
	}
}

// log and collect the results after instructions have been dispatched to bots
func logAndCollectResults(
	numBots int,
	resultsChan chan DispatchResult,
	symbol string,
) {
	numErr := 0
	numOrder := 0
	numNoTradeDecision := 0

	for i := 0; i < numBots; i++ {
		// for each bot, get the result and then parse errors
		res := <-resultsChan

		for _, err := range res.Errs {
			if err != nil {
				// check if the error is a no trade decision
				if strings.HasPrefix(err.Error(), "no trade decision:") {
					numNoTradeDecision++
				} else {
					numErr++
				}

				log.Printf("%s : %s : %v", res.UserHash, symbol, err)
			} else {
				numOrder++

				log.Printf("%s : %s : order success", res.UserHash, symbol)
			}
		}
	}

	// log stats
	msg := fmt.Sprintf("orders: %d, errors: %d", numOrder, numErr)
	if numNoTradeDecision > 0 {
		msg += fmt.Sprintf(", no trade decisions: %d", numNoTradeDecision)
	}
	log.Println(msg)
}

// create an OpenUpdate
func getOpenUpdate(upd cls.Update) (cls.OpenUpdate, error) {
	// get Prices for all exchanges
	pricesResults := make(chan cls.PricesRes, 1)
	go getPrices(upd.Symbol, pricesResults)

	// complete the OpenUpdate with the collected Prices
	res := <-pricesResults
	if res.PanicErr != nil {
		return cls.OpenUpdate{}, fmt.Errorf("failed to get prices, %v", res.PanicErr)
	}
	log.Printf("successfully got prices for %s, %s", upd.Symbol, res.Prices)

	openUpdate := OpenUpdate{
		Upd:                  upd,
		SymbolPrices:         res.Prices,
		TraderSize:           decimal.NewFromFloat(upd.BIN_qty).Mul(upd.Entry),
		ExcSpreadDiffPercent: SpreadDiffs{},
	}

	// set spread differences
	for excName, excPrice := range res.Prices {
		if !excPrice.IsSupported {
			// .Market is 0 => avgPrice will be 0. Hence the calcs below would panic
			// so we give it placeholder values of 0
			openUpdate.ExcSpreadDiffPercent[excName] = decimal.Zero
			continue
		}
		// here we calculate the spread difference as a proportion of the average
		// price, not as a proportion of the binance or (eg) bybit price. this
		// means that we get more of a symmetric and balanced calculation.
		avgPrice := upd.Entry.Add(excPrice.Market).Div(decimal.NewFromInt(2))

		difference := upd.Entry.Sub(excPrice.Market)
		diffAsPercent := difference.Div(avgPrice).Mul(decimal.NewFromInt(100))

		openUpdate.ExcSpreadDiffPercent[excName] = diffAsPercent.Abs()
	}

	return openUpdate, nil
}

// create a change update
func getChangeUpdate(upd cls.Update) (cls.ChangeUpdate, error) {
	// get Prices for all exchanges
	pricesResults := make(chan cls.PricesRes, 1)
	go getPrices(upd.Symbol, pricesResults)

	res := <-pricesResults
	if res.PanicErr != nil {
		return cls.ChangeUpdate{}, fmt.Errorf("panic getting prices, %v", res.PanicErr)
	}

	isIncrease := upd.IsIncrease()

	return cls.ChangeUpdate{
		Upd:          upd,
		SymbolPrices: res.Prices,
		IsIncrease:   isIncrease,
	}, nil
}

// given an update, open the position for the list of given bots
func openPositions(upd cls.Update, bots []*Bot) {
	defer handleMultiBotPanic(upd, bots, true)

	openUpdate, err := getOpenUpdate(upd)
	if err != nil {
		panic(fmt.Errorf("failed to get open update, %v", err))
	}

	// make sure that there is symbol data for this position
	var wg sync.WaitGroup
	for excName := range openUpdate.SymbolPrices {
		_, hasSymbolData := handler.excData[excName]
		if !hasSymbolData {
			wg.Add(1)
			go func(excName string) {
				defer wg.Done()

				// get symbol data for this symbol
				setSpecificSymbolExchangeData(excName, upd.Symbol)
			}(excName)
		}
	}
	wg.Wait()

	// dispatch to bots, create storage for results
	numBots := len(bots)
	resultsChan := make(chan DispatchResult, numBots)

	// send to all bots
	for _, bot := range bots {
		go func(bot *Bot) {
			defer handleSingleBotPanic(bot, resultsChan)

			// open a position for the bot, then send the opened to chan
			opened, errs := bot.openPosition(openUpdate)
			if !opened {
				bot.ignore(openUpdate.Upd.Symbol)
			}

			resultsChan <- DispatchResult{cls.GetBotHash(bot.UserID, bot.BotID), errs}
		}(bot)
	}

	logAndCollectResults(numBots, resultsChan, upd.Symbol)
}

// close the position for every bot in the list
func closePositions(upd cls.Update, bots []*Bot) {
	defer handleMultiBotPanic(upd, bots, false)

	numBots := len(bots)
	resultsChan := make(chan DispatchResult, numBots)

	// send to all bots
	for _, bot := range bots {
		go func(bot *Bot) {
			defer handleSingleBotPanic(bot, resultsChan)

			// close the position for the bot, then send the result to chan
			didClose, results := bot.closePosition(upd, false)

			isTradeFail := !didClose &&
				len(results) > 0 &&
				results[0] != nil &&
				!strings.HasPrefix(results[0].Error(), "no trade decision")

			if isTradeFail {
				bot.ignore(upd.Symbol)

				config, err := bot.getConfig()
				if err != nil {
					common.SendStaffAlert(fmt.Sprintf(
						"For user/bot %s, failed to read bot config, %v\nFailed to close position, results: %v",
						bot.getHash(), err, results,
					))
				} else {
					bot.sendAlertNotif(fmt.Sprintf(
						"Failed to close your position in %s, please check your positions.",
						upd.Symbol,
					), config)
				}
			}
			resultsChan <- DispatchResult{cls.GetBotHash(bot.UserID, bot.BotID), results}
		}(bot)
	}

	logAndCollectResults(numBots, resultsChan, upd.Symbol)
}

// run changePosition for a list of bots
func changePositions(upd cls.Update, bots []*Bot) {
	defer handleMultiBotPanic(upd, bots, false)

	changeUpd, err := getChangeUpdate(upd)
	if err != nil {
		panic("error getting prices")
	}

	isORisNot := "is not"
	if changeUpd.IsIncrease {
		isORisNot = "is"
	}
	log.Printf(
		"update %s %s (to qty: %s) %s an increase",
		upd.Side, upd.Symbol, cls.FmtFloat(upd.BIN_qty), isORisNot,
	)

	// dispatch to bots
	numBots := len(bots)
	resultsChan := make(chan DispatchResult, numBots)

	for _, bot := range bots {
		go func(bot *Bot) {
			defer handleSingleBotPanic(bot, resultsChan)

			// close the position for the bot, then send the result to chan
			_, result := bot.changePosition(changeUpd)
			resultsChan <- DispatchResult{cls.GetBotHash(bot.UserID, bot.BotID), result}
		}(bot)
	}

	logAndCollectResults(numBots, resultsChan, upd.Symbol)
}

func getPanicMsg(r interface{}) string {
	stackTrace := make([]byte, 4096)
	length := runtime.Stack(stackTrace, true)
	errMsg := fmt.Sprintf("panic: %v\n%s", r, stackTrace[:length])

	return errMsg
}
