package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	"github.com/lightspeed-trading/app/db"
	"github.com/shopspring/decimal"
)

// a bot is a currently copying instance that a user is 'running'
type Bot struct {
	UserID         string                 // the users id
	BotID          int                    // the bot id, concat with userID for primary key
	Exchange       string                 // must be in the list of exchanges in classes/exchange.go
	Positions      map[string]BotPosition // map of positions the bot is actively tracking for trades. string key: cls.GetPosHash
	PosMtx         sync.Mutex             // locked when manual changes are being checked before a trade is made
	ExcClient      ExchangeClient         // exchange client
	OpenDelayUntil time.Time              // cannot open positions until this time
	CurBalance     Decimal                // the accounts current balance
	IgnoreList     []string               // list of symbols that the bot is curerntly ignoring
	PnlWatching    []string               // list of symbols that pnl is being got for currently
	PnlWatchingMtx sync.Mutex             // mutex for PnlWatching
	Trader         string                 // the trader that the bot is copying
	InitialBalance Decimal                // the starting balance of the user
	Logs           cls.BotLogs            // the most recent logs for a bot
}

type CreateOrderParam struct {
	Side           string `json:"side"`
	Symbol         string `json:"symbol"`
	OrderType      string `json:"order_type"`
	Qty            string `json:"qty"`
	TimeInForce    string `json:"time_in_force"`
	ReduceOnly     bool   `json:"reduce_only"`
	CloseOnTrigger bool   `json:"close_on_trigger"`

	Price       *Decimal `json:"price,omitempty"`
	TakeProfit  *Decimal `json:"take_profit,omitempty"`
	StopLoss    *Decimal `json:"stop_loss,omitempty"`
	PositionIdx *int     `json:"position_idx,omitempty"`
}

// qty is the qty that was ordered, res: "200" if it was a success, otherwise it's
// the error code & message given by the exchange
type OrderRes struct {
	qty        Decimal
	resStr     string
	req        CreateOrderParam
	orderStart time.Time
}

type TPSL struct {
	TP *Decimal
	SL *Decimal
}

var BotErrLog = "Bot error."

// gets the bots hash
func (bot *Bot) getHash() string {
	return cls.GetBotHash(bot.UserID, bot.BotID)
}

func getTPSL(symbol string, side string, entry Decimal, exchange string, config db.BotConfig) TPSL {
	var tpPtr *Decimal
	var slPtr *Decimal

	if !config.AutoTP.Equal(decimal.NewFromInt(-1)) {
		tpFloat := getLimitPrice(symbol, side, entry, config.AutoTP, "tp", exchange)
		tpPtr = &tpFloat
	}
	if !config.AutoSL.Equal(decimal.NewFromInt(-1)) {
		slFloat := getLimitPrice(symbol, side, entry, config.AutoSL, "sl", exchange)
		slPtr = &slFloat
	}

	return TPSL{tpPtr, slPtr}
}

func (bot *Bot) getConfig() (db.BotConfig, error) {
	return getConfig(bot.UserID, bot.BotID)
}

func getConfig(userID string, botID int) (db.BotConfig, error) {
	return db.ReadRecordCPYT(dbConnPool, userID, botID)
}

// given an order result, make an error string that includes significant data
func (res OrderRes) getErrStr() string {
	price := "no"
	if res.req.Price != nil {
		price = fmt.Sprint(*res.req.Price)
	}
	tp := "no"
	if res.req.TakeProfit != nil {
		tp = fmt.Sprint(*res.req.TakeProfit)
	}
	sl := "no"
	if res.req.StopLoss != nil {
		sl = fmt.Sprint(*res.req.StopLoss)
	}

	return fmt.Sprintf(
		"err:%s\n, side:%s, symbol: %s, qty: %s, price: %s, tp: %s, sl: %s, reduceOnly: %t",
		res.resStr,
		res.req.Side,
		res.req.Symbol,
		res.req.Qty,
		price,
		tp,
		sl,
		res.req.ReduceOnly,
	)
}

// given a list of order results, make a list of errors
func makeErrorListFromOrderRes(results []OrderRes) []error {
	numOrder := len(results)
	errList := make([]error, numOrder)

	for i := 0; i < numOrder; i++ {
		res := results[i]
		if res.resStr == "200" {
			errList[i] = nil
		} else {
			errList[i] = errors.New(res.getErrStr())
		}
	}

	return errList
}

// add a symbol to the ignore list
func (bot *Bot) ignore(symbol string) {
	// check if its already ignored, to prevent double ignores
	if bot.isIgnored(symbol) {
		log.Printf("%s : not ignoring %s, already ignored", bot.getHash(), symbol)
		return
	}

	bot.IgnoreList = append(bot.IgnoreList, symbol)
	log.Printf("%s : %s added to ignore list", bot.getHash(), symbol)
}

// remove from ignore list
func (bot *Bot) unIgnore(symbol string) {
	found := false
	for ind, v := range bot.IgnoreList {
		if v == symbol {
			// append everything before i and everything after i
			bot.IgnoreList = append(bot.IgnoreList[:ind], bot.IgnoreList[ind+1:]...)
			found = true
			break
		}
	}

	// log for debugging
	if !found {
		log.Printf("%s : failed to remove %s from ignore list", bot.getHash(), symbol)
	} else {
		log.Printf("%s : removed %s from ignore list", bot.getHash(), symbol)
	}
}

// check if a symbol is ignored
func (bot *Bot) isIgnored(symbol string) bool {
	for _, sym := range bot.IgnoreList {
		if symbol == sym {
			return true
		}
	}
	return false
}

// check if a given symbol is blacklisted, given a config
func symbolIsBlacklisted(symbol string, config db.BotConfig) bool {
	for _, sym := range config.BlacklistCoins {
		if symbol == sym {
			return true
		}
	}
	return false
}

// check if a given symbol is whitelisted, given a config
func symbolIsWhitelisted(symbol string, config db.BotConfig) bool {
	if len(config.WhitelistCoins) == 0 {
		// whitelist disabled if theres none in it
		return true
	}

	for _, sym := range config.WhitelistCoins {
		if symbol == sym {
			return true
		}
	}
	return false
}

// get a valid price according to the price step of the symbol
func getValidPrice(symbol string, price Decimal, exchange string) Decimal {
	priceStep := handler.excData[exchange].SymbolData[symbol].PriceStep

	// calculate valid price
	numSteps := price.Div(priceStep).Round(0).IntPart()          // number of price steps that fit into price
	validPriceDec := priceStep.Mul(decimal.NewFromInt(numSteps)) // multiply by price step to get a valid price, as close as possible to the ideal value

	return validPriceDec
}

// get the limit price (SL or TP) for a trade, return a float64
// limit must be "tp" or "sl"
func getLimitPrice(
	symbol string,
	side string,
	entry Decimal,
	limPercent Decimal,
	limit string,
	exchange string,
) Decimal {
	// pDelta is the price difference between the market price and the limit,
	// whether that limit is a tp or an sl.
	pDelta := (limPercent.Div(decimal.NewFromInt(100))).Mul(entry)
	if limit == "sl" {
		// switch sign of delta its a stop loss
		pDelta = pDelta.Mul(decimal.NewFromInt(-1))
	}

	// calculate ideal limit based on side
	var idealLimit Decimal
	if side == "Sell" {
		idealLimit = entry.Sub(pDelta)
	} else {
		idealLimit = entry.Add(pDelta)
	}

	// get a valid price
	return getValidPrice(symbol, idealLimit, exchange)
}

// given a float qty of a symbol, return a list of valid orders that could be made
// to order that qty.
func getBulkQtyList(qty Decimal, symbol string, exchange string) []Decimal {
	minQty := handler.excData[exchange].SymbolData[symbol].MinQty
	maxQty := handler.excData[exchange].SymbolData[symbol].MaxQty
	qtyStep := handler.excData[exchange].SymbolData[symbol].QtyStep

	// determine how many max qtys are required
	numMax := qty.Div(maxQty).Floor()

	// make a slice with numMax elements all set to maxQty
	maxes := make([]Decimal, numMax.IntPart())
	for i := range maxes {
		maxes[i] = maxQty
	}

	// determine the remainder, return straight away if remainder is 0
	remainder := qty.Sub((maxQty.Mul(numMax)))
	if remainder.LessThan(minQty) {
		return maxes
	}

	// determine a remainder thats valid for the exchange
	numSteps := remainder.Div(qtyStep).Floor()
	validRemainder := numSteps.Mul(qtyStep)

	return append(maxes, validRemainder)
}

// return a list of errors containing just 1 error, when the bot has decided
// not to open a position
func getNoTradeErrsF(desc string, args ...any) []error {
	descF := fmt.Sprintf(desc, args...)

	return []error{fmt.Errorf("no trade decision: %s", descF)}
}

// Top level func that opens a position from an Update.
// Must notify the user on error.
// Need to confirm that bybit has the coin being traded, before this func is called.
// Returned bool is true if orders were submitted, returned []error are any
// errors that were hit. All results must be logged.
func (bot *Bot) openPosition(upd OpenUpdate) (bool, []error) {
	bot.PosMtx.Lock()
	defer bot.PosMtx.Unlock()

	symbol := upd.Upd.Symbol
	side := upd.Upd.Side

	// get the bots settings config
	config, err := bot.getConfig()
	if err != nil {
		bot.sendErrorNotif(BotErrLog, db.BotConfig{})
		bot.Logs.Logf(BotErrLog)
		return false, []error{fmt.Errorf("failed to get config, %v", err)}
	}

	// check the OpenUpdate.SymbolPrices, to see if the symbol is supported on
	// this bots selected exchange.
	if !upd.SymbolPrices[bot.Exchange].IsSupported {
		msg := fmt.Sprintf(
			"Not opening %s, it is not supported on %s.",
			symbol, bot.Exchange,
		)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("unsupported symbol")
	}

	// switch if inverse
	if config.ModeInverse {
		side = cls.SwitchSide(side)
	}

	// check if there is already a position open in this symbol
	_, isOpen := bot.Positions[cls.GetPosHash(symbol, side)]
	if isOpen {
		msg := fmt.Sprintf("Not opening %s, you already have a position open in it.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("position already exists")
	}

	// check config settings - can the bot open a position?
	// check if a position can be opened according to user settings
	if config.ModeCloseOnly {
		msg := fmt.Sprintf("Not opening %s, you are currently in close only mode.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("in close only mode")
	}

	// check if trader size is small
	if !config.MinTraderSize.Equal(decimal.NewFromInt(-1)) && config.MinTraderSize.GreaterThan(upd.TraderSize) {
		msg := fmt.Sprintf("Not opening %s, size is below your minimum trader size.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("below min trader size (%s)", config.MinTraderSize)
	}

	// check we are below max position count
	if config.MaxOpenPositions != -1 && len(bot.Positions) >= config.MaxOpenPositions {
		msg := fmt.Sprintf("Not opening %s, you currently have your maximum amount of positions open.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("at max positions (%d)", config.MaxOpenPositions)
	}

	// check if the add delay is in action
	if isSettingEnabled(config.OpenDelay) && time.Now().Before(bot.OpenDelayUntil) {
		msg := fmt.Sprintf("Not opening %s, open delay is currently in action.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("open delay active")
	}

	// check if the coin is blacklisted
	if symbolIsBlacklisted(symbol, config) {
		msg := fmt.Sprintf("Not opening %s, you have blacklisted it.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("symbol blacklisted")
	}

	// check if coin is not whitelisted
	if !symbolIsWhitelisted(symbol, config) {
		msg := fmt.Sprintf("Not opening %s, you have not whitelisted it.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("not whitelisted")
	}

	// check price difference is not too great
	spreadDiff := upd.ExcSpreadDiffPercent[bot.Exchange]

	spreadDiffTooGreat := spreadDiff.GreaterThanOrEqual(config.ExcSpreadDiffPercent)
	if isSettingEnabled(config.ExcSpreadDiffPercent) && spreadDiffTooGreat {
		msg := fmt.Sprintf("Not opening %s, price difference between exchanges is too great.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF(
			"spread difference (set at %s) too great",
			config.ExcSpreadDiffPercent,
		)
	}

	// rough check if usdt balance is too low
	openBalance := bot.InitialBalance.Mul(config.InitialOpenPercent).Div(decimal.NewFromInt(100))
	if bot.CurBalance.LessThan(openBalance) {
		msg := fmt.Sprintf("Not opening %s, wallet balance is too low.", symbol)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF(
			"wallet balance (%s) too low",
			bot.CurBalance,
		)
	}

	// initial settings check complete, begin order creation process
	// determine the correct leverage to use
	markPrice := upd.SymbolPrices[bot.Exchange].Market
	maxLev := handler.excData[bot.Exchange].SymbolData[symbol].MaxLeverage
	leverage := decimal.Min(config.Leverage, maxLev)

	// determine the ideal qty to purchase as a float (unadjusted for tick sizes)
	symbolQty := (config.InitialOpenPercent.Div(decimal.NewFromInt(100)).Mul(bot.InitialBalance)).Div(markPrice).Mul(leverage)
	minQty := handler.excData[bot.Exchange].SymbolData[symbol].MinQty

	// check if the qty is below the minimum order qty
	if symbolQty.LessThan(minQty) {
		msg := fmt.Sprintf(
			"Not opening %s, position would be below the minimum order quantity.",
			symbol,
		)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("order would be below min qty")
	}

	// get tp and sl
	tpsl := getTPSL(symbol, side, markPrice, bot.Exchange, config)

	// check user leverage is not too high
	useLev := config.Leverage
	symbolMaxLev := handler.excData[bot.Exchange].SymbolData[symbol].MaxLeverage
	if useLev.GreaterThan(symbolMaxLev) {
		msg := fmt.Sprintf(
			"Your max leverage %s is too high, so we are opening the position with the symbol max of %s",
			config.Leverage, symbolMaxLev,
		)
		bot.Logs.Logf(msg)
		log.Printf("%s : %s", bot.getHash(), msg)

		useLev = symbolMaxLev
	}

	// set leverage
	err = bot.ExcClient.setLeverage(symbol, useLev, true)
	if err != nil {
		bot.Logs.Logf(
			"Not opening %s, failed to set leverage to %s",
			symbol, leverage,
		)
		errReturn := fmt.Errorf("failed to set leverage, %v", err)
		return false, []error{errReturn}
	}

	// send order
	results := bot.ExcClient.placeOrder(symbol, side, symbolQty, false, tpsl.TP, tpsl.SL)

	// post opening the position jobs
	// work out how much qty was sucessfully ordered
	qtyOrdered := float64(0)
	for _, res := range results {
		if res.resStr == "200" {
			qtyOrdered += res.qty.InexactFloat64()
		}
	}
	numOrder := len(results)

	if qtyOrdered == 0 {
		// all orders failed
		errList := make([]error, numOrder)

		for i := 0; i < numOrder; i++ {
			res := results[i]
			errList[i] = errors.New(res.getErrStr())
		}
		return false, errList

	} else {
		// get position data for the new pos
		newPos, err := bot.ExcClient.getMyPosition(symbol)
		if err != nil {
			msg := fmt.Sprintf("After placing orders for %s, ByBit failed to give us more data.", symbol)

			bot.sendErrorNotif(msg, config)
			bot.Logs.Logf(msg)

			return false, []error{fmt.Errorf("failed to get pos data after ordering, %v", err)}
		}
		delayTime := time.Duration(config.AddDelay.Mul(decimal.NewFromInt(100)).IntPart()) * time.Millisecond
		newPos.IgnoreAddsUntil = time.Now().Add(delayTime)

		// send ping
		go bot.sendPositionOpenedNotif(newPos, config.NotifyDest, config.BotName)

		bot.Logs.Logf("Success opening position in %s %s", side, symbol)

		// add to position list
		bot.Positions[cls.GetPosHash(symbol, side)] = newPos

		// make and return an errorlist from the order results
		return true, makeErrorListFromOrderRes(results)
	}
}

// close a position that the bot has open. Returned bool is true if orders
// were submitted, returned []error is the erorrs that it came accross.
// if the forceClose flag is enabled, then the function will not check
// against bot.Positions to see if the position exists or not. Mostly used
// in testing.
func (bot *Bot) closePosition(upd cls.Update, forceClose bool) (bool, []error) {
	bot.PosMtx.Lock()
	defer bot.PosMtx.Unlock()

	symbol := upd.Symbol

	// remove from ignore list
	if bot.isIgnored(symbol) {
		bot.unIgnore(symbol)
		return false, getNoTradeErrsF("symbol is ignored")
	}

	// get the bots settings config
	config, err := bot.getConfig()
	if err != nil {
		bot.sendErrorNotif("Failed to get settings.", db.BotConfig{})
		return false, []error{fmt.Errorf("failed to get config, %v", err)}
	}

	// switch if inverse
	if config.ModeInverse {
		upd.SwitchSide()
	}

	// check if pos has already been closed
	pos, posExists := bot.Positions[cls.GetPosHash(symbol, upd.Side)]
	if !posExists && !forceClose {
		bot.sendAlertNotif(
			fmt.Sprintf("Not closing %s, it has already been closed.", symbol),
			config,
		)
		return false, getNoTradeErrsF("position already closed")
	}

	// check if in no closes mode
	if config.ModeNoClose {
		bot.sendAlertNotif(
			fmt.Sprintf("Not closing %s, you have enabled the no closes mode.", symbol),
			config,
		)
		return false, getNoTradeErrsF("no closes mode enabled")
	}

	// get side for order
	side := cls.SwitchSide(pos.Side) // pos.Side not upd.Side, to account for inverse mode

	// send close position order
	results := bot.ExcClient.placeOrder(
		pos.Symbol,
		side,
		pos.Qty,
		true,
		nil,
		nil,
	)

	// check if EVERY error is 130125 - position qty already 0, cannot reduce
	all130125 := true
	someGood := false

	for _, res := range results {
		if res.resStr == "200" {
			someGood = true
		} else if !strings.HasPrefix(res.resStr, "130125") {
			all130125 = false
		}
	}

	// set doPing for onSuccessfullClose
	doPing := false
	if someGood || all130125 {
		doPing = true
	}

	bot.Logs.Logf("Success closing %s %s", upd.Side, upd.Symbol)

	go bot.onSuccessfullClose(pos, doPing, config.NotifyDest, results[0].orderStart, config.BotName)

	// return a []error
	if all130125 {
		return true, []error{nil}
	} else {
		return true, makeErrorListFromOrderRes(results)
	}
}

// runs after a position is closed, and should always be ran as a go func
func (bot *Bot) onSuccessfullClose(
	pos BotPosition,
	doPing bool,
	whUrl string,
	orderStart time.Time,
	botName string,
) {
	// readjust wallet balance
	errChan := make(chan error)
	go func() {
		errChan <- bot.setBalance()
	}()

	// remove from positions
	posHash := cls.GetPosHash(pos.Symbol, pos.Side)
	delete(bot.Positions, posHash)

	// send a ping
	if doPing {
		// get pnl
		pnl, err := bot.getPnl(pos.Symbol, orderStart.Unix(), pos.Qty)
		if err != nil {
			log.Printf("%s : failed to get pnl, %v", bot.getHash(), err)
		}

		// send the order ping
		go bot.sendPosClosedNotif(pos, pnl, whUrl, botName)
	}

	// receive err from setting balance
	if err := <-errChan; err != nil {
		// dont alert user but do log the issue
		log.Printf("%s : failed to readjust wallet balance, %v", bot.UserID, err)
	}
}

// change a position (increase or decrease qty). returned bool is whether
// orders were submitted or not
func (bot *Bot) changePosition(upd ChangeUpdate) (bool, []error) {
	bot.PosMtx.Lock()
	defer bot.PosMtx.Unlock()

	side := upd.Upd.Side
	symbol := upd.Upd.Symbol

	// get config
	config, err := bot.getConfig()
	if err != nil {
		bot.sendErrorNotif("Bot error.", db.BotConfig{})
		bot.Logs.Logf("Failed to access settings")
		return false, []error{fmt.Errorf("failed to get config, %v", err)}
	}

	// switch if inverse
	if config.ModeInverse {
		side = cls.SwitchSide(side)
	}
	var tradeDescriptor string
	if upd.IsIncrease {
		tradeDescriptor = fmt.Sprintf("buying more of %s %s", side, symbol)
	} else {
		tradeDescriptor = fmt.Sprintf("selling part of %s %s", side, symbol)
	}

	// check ignore list
	if bot.isIgnored(symbol) {
		bot.Logs.Logf("Not %s, symbol is currently ignored", tradeDescriptor)
		return false, getNoTradeErrsF("symbol currently ignored")
	}

	// check no sells mode is enabled
	if !upd.IsIncrease && config.ModeNoSells {
		msg := fmt.Sprintf("Not %s, you have no sells mode enabled.", tradeDescriptor)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, getNoTradeErrsF("no sells mode enabled")
	}

	// work out the qty to purchase
	posHash := cls.GetPosHash(symbol, side)
	thisPos, exists := bot.Positions[posHash]
	if !exists {
		log.Printf(
			"%s : %d : can't (%s), position hash (%s) does not exist",
			bot.UserID, bot.BotID, tradeDescriptor, posHash,
		)

		return false, []error{errors.New("couldnt find position")}
	}

	old_bin_qty := upd.Upd.Old_BIN_qty
	new_bin_qty := upd.Upd.BIN_qty

	ratio := decimal.NewFromFloat(new_bin_qty / old_bin_qty)

	if isSettingEnabled(config.MaxAddMultiplier) && upd.IsIncrease && ratio.GreaterThan(config.MaxAddMultiplier) {
		// only reduce the ratio if the user is increasing. for a sell keep the ratio at whatever the trader did
		ratio = config.MaxAddMultiplier
		msg := fmt.Sprintf(
			"Using your max add multiplier of %s",
			ratio,
		)
		log.Printf("%s : %s", bot.getHash(), msg)
		bot.Logs.Logf(msg)
	}

	new_bot_qty := thisPos.Qty.Mul(ratio)
	orderQty := new_bot_qty.Sub(thisPos.Qty).Abs()

	// check its valid to order that qty
	minQty := handler.excData[bot.Exchange].SymbolData[symbol].MinQty
	if orderQty.LessThan(minQty) {
		msg := fmt.Sprintf("Not %s, the order would be lower than the minimum order quantity", tradeDescriptor)

		bot.sendAlertNotif(msg, config)
		bot.Logs.Logf(msg)

		return false, []error{errors.New("order would be below min qty")}
	}

	var orderStart time.Time
	var results []OrderRes
	if upd.IsIncrease {
		curPrice := upd.SymbolPrices[bot.Exchange].Market
		entry := thisPos.Entry

		// check if it was opened too recently
		if thisPos.IgnoreAddsUntil.After(time.Now()) {
			msg := fmt.Sprintf("Not %s, the position was opened too recently", tradeDescriptor)

			bot.sendAlertNotif(msg, config)
			bot.Logs.Logf(msg)

			return false, getNoTradeErrsF("add delay - position opened too recently")
		}

		// check if its too close to entry
		if isSettingEnabled(config.AddPreventionPercent) {

			diffToEntry := (curPrice.Sub(entry))
			diffPercent := diffToEntry.Div(curPrice).Mul(OneHundred)

			if diffPercent.Abs().LessThan(config.AddPreventionPercent) {
				msg := fmt.Sprintf("Not %s, price is too close to entry.", tradeDescriptor)

				bot.sendAlertNotif(msg, config)
				bot.Logs.Logf(msg)

				return false, getNoTradeErrsF(
					"market price (%s) too close to entry (%s) for users config (%s)",
					curPrice, entry, config.AddPreventionPercent,
				)
			}
		}

		// check if price is above entry
		if config.BlockAddsAboveEntry {
			var priceAboveEntry bool
			if side == "Buy" {
				priceAboveEntry = curPrice.GreaterThan(entry)
			} else {
				priceAboveEntry = curPrice.LessThan(entry) // for a short, market < entry is technically 'above'
			}

			if priceAboveEntry {
				msg := fmt.Sprintf(
					"Not %s, market price (%s) is above entry (%s).",
					tradeDescriptor, curPrice, entry,
				)

				bot.sendAlertNotif(msg, config)
				bot.Logs.Logf(msg)

				return false, getNoTradeErrsF(
					"for side (%s), market price (%s) above entry (%s)",
					side, curPrice, entry,
				)
			}
		}

		// make sure that adding to the pos will not make it too much of the portfolio
		if isSettingEnabled(config.OneCoinMaxPercent) {

			newUsdtValue := (orderQty.Add(thisPos.Qty)).Mul(curPrice).Div(thisPos.Leverage)
			maxUsdtValue := bot.InitialBalance.Mul(config.OneCoinMaxPercent).Div(OneHundred)

			if newUsdtValue.GreaterThan(maxUsdtValue) {
				msg := fmt.Sprintf(
					"Not %s, the position would be larger than your max position size.",
					tradeDescriptor,
				)

				bot.sendAlertNotif(msg, config)
				bot.Logs.Logf(msg)

				return false, getNoTradeErrsF(
					"new pos size (%s) would be larger than max position size (%s)",
					newUsdtValue, maxUsdtValue,
				)
			}
		}

		// work out new TP and SL
		newEntry := (thisPos.Qty.Mul(thisPos.Entry).Add(orderQty.Mul(curPrice))).Div(thisPos.Qty.Add(orderQty))
		tpsl := getTPSL(symbol, side, newEntry, bot.Exchange, config)

		// place order
		orderStart = time.Now()
		results = bot.ExcClient.placeOrder(symbol, side, orderQty, false, tpsl.TP, tpsl.SL)

	} else {
		// position has been decreased
		// check if the qty will go below 0
		if thisPos.Qty.Sub(orderQty).IsNegative() {
			orderQty = thisPos.Qty
		}

		// place order
		reverseSide := cls.SwitchSide(side)
		results = bot.ExcClient.placeOrder(symbol, reverseSide, orderQty, true, nil, nil)
	}

	// add / reduce from stored positions // TODO #15 do properly, code debt
	apiPos, err := bot.ExcClient.getMyPosition(symbol)
	if err != nil {
		bot.sendErrorNotif(fmt.Sprintf("After placing orders for %s, ByBit failed to give us more data.", symbol), config)
		return false, []error{fmt.Errorf("failed to get pos data after ordering, %v", err)}
	}

	ourPos := bot.Positions[posHash]
	ourPos.Qty = apiPos.Qty
	ourPos.Entry = apiPos.Entry

	bot.Positions[posHash] = ourPos
	/*
		// THE BETTER WAY OF DOING IT (THAT DOESNT ALWAYS WORK)
		mult := 1.0
		if !upd.IsIncrease {
			mult = -1.0
		}
		for _, res := range results {
			qty, _ := res.qty.Float64()
			nPos := bot.Positions[posHash]
			nPos.Qty = bot.Positions[posHash].Qty + (mult * qty)

			bot.Positions[posHash] = nPos
		}
	*/

	bot.Logs.Logf("Success %s", tradeDescriptor)

	// send a ping
	if upd.IsIncrease {
		go bot.sendPositionAddedToPing(ourPos, config.NotifyDest,
			config.BotName)

	} else if time.Since(orderStart) > time.Minute {
		go bot.sendPositionReducedPing(ourPos, nil, config.NotifyDest,
			config.BotName)

	} else {
		go func() {
			// get pnl
			pnl, err := bot.getPnl(ourPos.Symbol, orderStart.Unix(),
				ourPos.Qty)

			if err != nil {
				// pnl == nil (if err != nil), so no need to explicitly
				// give pnl == nil in the following function call
				go common.LogAndSendAlertF(
					"%s : failed to get pnl, %v", bot.getHash(), err,
				)
			}

			bot.sendPositionReducedPing(ourPos, pnl, config.NotifyDest,
				config.BotName)
		}()
	}

	return true, makeErrorListFromOrderRes(results)
}

// check if theres been any manual changes to the bots positions. Top level func,
// must log errors & not propagate them. Has a secondary function of
// checking if API keys, etc, are still correct.
func (bot *Bot) checkManualChanges(ctx context.Context, pool *pgxpool.Pool) {
	defer func() {
		if err := recover(); err != nil {
			errMsg := getPanicMsg(err)
			go common.SendStaffAlert(errMsg)
		}
	}()

	// return if currently locked - a position is currently being altered
	canLock := bot.PosMtx.TryLock()
	if !canLock {
		return
	} else {
		bot.PosMtx.Unlock()
	}

	// make api request with a go func so it can be cancelled
	type posAndErr struct {
		positions map[string]cls.BotPosition
		err       error
	}
	apiPosChan := make(chan posAndErr, 1)

	go func() {
		apiPositions, err := bot.ExcClient.getMyPositions()
		apiPosChan <- posAndErr{apiPositions, err}
	}()

	// wait for either cancel func to be called, or api request to return
	var res posAndErr
	select {
	case posErr := <-apiPosChan:
		res = posErr
	case <-ctx.Done():
		return
	}

	if res.err != nil {
		log.Printf("%s : failed to get position list, %v", bot.getHash(), res.err)
		if strings.HasSuffix(res.err.Error(), "10003, API key is invalid.") {
			// remove from handler
			trUid, err := bot.findUid()
			if err == nil {
				handler.monsMtx.Lock()
				defer handler.monsMtx.Unlock()

				mon := handler.runningMons[trUid]
				mon.removeBotByHash(bot.getHash())
			}

			// send alert to user that their api keys have failed
			config, err := getConfig(bot.UserID, bot.BotID)
			if err != nil {
				log.Printf("%s : failed to get config, %v", bot.UserID, err)
			}
			_sendNotif(
				"Crash Alert",
				"Your API keys are invalid. Please generate new ones.",
				common.DiscordAlertColourBotError,
				bot.getBasicNotifAttrs(config.BotName),
				config.NotifyDest,
				bot.UserID,
				bot.BotID,
			)

			// deactivate bot
			db.SetBotActivity(bot.UserID, bot.BotID, db.BotInactive, pool)
		}
		return
	}

	// check positions for differences with api return value
	apiPositions := res.positions

	bot.PosMtx.Lock()
	defer bot.PosMtx.Unlock()

	toDelete := []string{}
	for posHash, pos := range bot.Positions {
		apiPos, apiPosExists := apiPositions[posHash]

		// check if position has been sold
		if !apiPosExists {
			go bot.onUnexpectedSell(pos)
			toDelete = append(toDelete, posHash)

			continue
		}

		// check if the quantity is different
		if pos.Qty != apiPos.Qty {
			log.Printf("%s : manual qty change for %s , %s -> %s", bot.getHash(), posHash, pos.Qty, apiPos.Qty)
			pos.Qty = apiPos.Qty
			pos.Entry = apiPos.Entry
		}

		// set tps
		pos.Tp = apiPos.Tp
		pos.Sl = apiPos.Sl

		bot.Positions[posHash] = pos
	}

	if len(toDelete) > 0 {
		go bot.setBalance()
	}

	// delete after finished iterating over them
	for _, posHash := range toDelete {
		delete(bot.Positions, posHash)
	}
}

// runs when a position is sold after either being manually closed, hit TP or SL.
// Should be started as a goroutine.
func (bot *Bot) onUnexpectedSell(pos BotPosition) {
	bot.ignore(pos.Symbol)

	// get config
	config, err := bot.getConfig()
	if err != nil {
		log.Printf("%s : failed to get config in onUnexpectedSell, %v", bot.getHash(), err)
		return
	}

	// get pnl
	pnl, err := bot.getPnl(pos.Symbol, time.Now().Add(-20*time.Second).In(time.UTC).Unix(), pos.Qty)
	if err != nil {
		log.Printf("%s : failed to get pnl, %v", bot.getHash(), err)
	}

	// send notification
	var desc string
	attrs := bot.getBasicNotifAttrs(config.BotName)
	if pnl != nil {
		attrs = addPnlToAttrs(attrs, pnl)
		desc = cls.GetPingDescription(pos.Side, pnl.Qty, pos.Symbol)
	} else {
		desc = cls.GetPingDescription(pos.Side, pos.Qty, pos.Symbol)
	}

	_sendNotif(
		"Position Closed (TP/SL/Manual)",
		desc,
		common.DiscordAlertColourClosePosition,
		attrs,
		config.NotifyDest,
		bot.UserID,
		bot.BotID,
	)
}

// find the trader uid a bot is on
func (bot *Bot) findUid() (string, error) {
	handler.monsMtx.Lock()
	defer handler.monsMtx.Unlock()

	for trUid, mon := range handler.runningMons {
		for _, b := range mon.bots {
			if bot.UserID == b.UserID && bot.BotID == b.BotID {
				return trUid, nil
			}
		}
	}

	return "", errors.New("no trader uid")
}

// check if a setting stored as a Decimal is enabled
func isSettingEnabled(value Decimal) bool {
	return !value.Equal(decimal.NewFromInt(-1))
}
