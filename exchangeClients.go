package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	"github.com/lightspeed-trading/app/phemex"
	"github.com/nat-echlin/bybit"
	"github.com/shopspring/decimal"
)

// an interface for any supported exchange
type ExchangeClient interface {
	// Gets my position data for all symblols. If includeZeroSizes is true,
	// the string (in the map) will be the symbol, if false it will be a
	// poshash got from cls.GetPosHash
	getMyPositions() (map[string]cls.BotPosition, error)

	// gets my position data for a specific symbol
	getMyPosition(symbol string) (BotPosition, error)

	// gets usdt balance
	getUSDTBalance() (Decimal, error)

	// get closed pnl data. closedAfter is a timestamp in seconds
	getClosedPNL(symbol string, closedAfter *int64) ([]ClosedPNL, error)

	// set the leverage for a symbol
	setLeverage(symbol string, leverage Decimal, ignoreAlreadySetError bool) error

	// Place an order on the exchange. If the given order qty is above the max order
	// qty for the given symbol, placeOrder will submit multiple orders (up to 10) so
	// that the entire qty is ordered.
	placeOrder(symbol string, side string, qty Decimal, reduceOnly bool, tp *Decimal, sl *Decimal) []OrderRes

	// get the curent market price of a symbol. Returned bool is whether the symbol
	// is supported or not on the exchange. -1 is returned in case of a symbol not
	// being supported, or an error.
	getPrice(symbol string) (Decimal, bool, error)

	// get symbol data for all symbols on an exchange
	getSymbolData() ([]Symbol, error)

	// Set the http client on the exchange client to be the next unused http client
	// to avoid getting ratelimited
	setNextClient()
}

// returns a new exchange client
func newExcClient(exchange string, testMode bool, key string, secret string) ExchangeClient {
	if exchange == cls.Bybit {
		var cli *bybit.Client
		if key == "" && testMode {
			cli = bybit.NewTestClient().Client
		} else if key == "" && !testMode {
			cli = bybit.NewClient()
		} else if key != "" && testMode {
			cli = bybit.NewTestClient().Client.WithAuth(key, secret)
		} else if key != "" && !testMode {
			cli = bybit.NewClient().WithAuth(key, secret)
		}

		return &bybitNormalised{
			cli: cli,
		}

	} else if exchange == cls.Phemex {
		return &phemexNormalised{
			cli: phemex.NewClient(testMode, key, secret),
		}

	} else {
		log.Panicf("received bad exchange (%s) to create client for", exchange)
		return nil
	}
}

type bybitNormalised struct {
	cli         *bybit.Client
	accountType *bybit.AccountType
}

func (byb *bybitNormalised) getMyPositions() (map[string]cls.BotPosition, error) {
	// create params
	resultsPerPage := 200
	usdt := bybit.CoinUSDT

	params := bybit.V5GetPositionInfoParam{
		Category:   "linear",
		Symbol:     nil,
		BaseCoin:   nil,
		SettleCoin: &usdt,
		Limit:      &resultsPerPage,
		Cursor:     nil,
	}

	// get bybit proxy resp
	byb.setNextClient()
	resp, err := byb.cli.V5().Position().GetPositionInfo(params)
	if err != nil {
		return nil, fmt.Errorf("failed to get response from bybit, %v", err)
	}

	// parse resp
	myPositions := map[string]BotPosition{}

	for _, pos := range resp.Result.List {
		botPos, err := botPosFromBybPos(pos)
		if err != nil {
			return nil, err
		}

		hash := botPos.GetHash()
		myPositions[hash] = botPos
	}

	return myPositions, nil
}

func (byb *bybitNormalised) getMyPosition(symbol string) (cls.BotPosition, error) {
	// create params
	resultsPerPage := 200

	params := bybit.V5GetPositionInfoParam{
		Category:   "linear",
		Symbol:     (*bybit.SymbolV5)(&symbol),
		BaseCoin:   nil,
		SettleCoin: nil,
		Limit:      &resultsPerPage,
		Cursor:     nil,
	}

	// get bybit resp
	byb.setNextClient()
	resp, err := byb.cli.V5().Position().GetPositionInfo(params)
	if err != nil {
		return cls.BotPosition{}, fmt.Errorf("failed to get response from bybit, %v", err)
	}

	// parse resp
	posList := resp.Result.List
	if len(posList) == 0 {
		return cls.BotPosition{}, fmt.Errorf("no positions returned for symbol %s", symbol)
	}

	bybPos := posList[0]
	pos, err := botPosFromBybPos(bybPos)
	if err != nil {
		return cls.BotPosition{}, err
	}

	return pos, nil
}

func (byb *bybitNormalised) getUSDTBalance() (Decimal, error) {
	byb.setAccountType()

	// get balance
	byb.setNextClient()
	res, err := byb.cli.V5().Account().GetWalletBalance(
		*byb.accountType, []bybit.Coin{bybit.CoinUSDT},
	)
	if err != nil {
		return decimal.Decimal{}, err
	}

	// extract float value from response and return it
	balResList := res.Result.List
	if len(balResList) == 0 {
		return decimal.Decimal{}, fmt.Errorf("got 0 results in result list, res: %v", res)
	}
	coinList := balResList[0].Coin
	if len(coinList) == 0 {
		return decimal.Decimal{}, fmt.Errorf("got 0 coins in coin list, res: %v", res)
	}

	balDec, err := decimal.NewFromString(coinList[0].WalletBalance)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("failed to parse Decimal from result string, %v", err)
	}

	return balDec, nil
}

func (byb *bybitNormalised) getClosedPNL(symbol string, closedAfter *int64) ([]ClosedPNL, error) {
	// create params
	if closedAfter != nil {
		closedAfterMs := *closedAfter * 1000
		closedAfter = &closedAfterMs
	}

	symbolV5 := bybit.SymbolV5(symbol)
	reqParam := bybit.V5GetClosedPnLParam{
		Category:  bybit.CategoryV5Linear,
		Symbol:    &symbolV5,
		StartTime: closedAfter,
	}

	// make request
	byb.setNextClient()
	res, err := byb.cli.V5().Position().GetClosedPnL(reqParam)
	if err != nil {
		return nil, fmt.Errorf("failed to make request, %v", err)
	}

	// create list of ClosedPnl
	closedPnls := []ClosedPNL{}
	for _, cloPnl := range res.Result.List {
		// parse all returned strings
		pnl, err := strconv.ParseFloat(cloPnl.ClosedPnl, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse pnl as float, %v", err)
		}
		qty, err := decimal.NewFromString(cloPnl.Qty)
		if err != nil {
			return nil, fmt.Errorf("failed to parse qty as decimal, %v", err)
		}
		avgExit, err := decimal.NewFromString(cloPnl.AvgExitPrice)
		if err != nil {
			return nil, fmt.Errorf("failed to parse avg exit price as decimal, %v", err)
		}

		closedPnls = append(closedPnls,
			ClosedPNL{
				Symbol:       symbol,
				Pnl:          pnl,
				Qty:          qty,
				AvgExitPrice: avgExit,
			},
		)
	}

	return closedPnls, nil
}

func (byb *bybitNormalised) setLeverage(
	symbol string,
	leverage Decimal,
	ignoreAlreadySetError bool,
) error {
	// create param
	lev := leverage.String()
	reqParam := bybit.V5SetLeverageParam{
		Category:     bybit.CategoryV5Linear,
		Symbol:       (bybit.SymbolV5)(symbol),
		BuyLeverage:  lev,
		SellLeverage: lev,
	}

	// send request
	byb.setNextClient()
	_, err := byb.cli.V5().Position().SetLeverage(reqParam)

	// check if the error is that the leverage was already set to the given value
	if err != nil {
		isAlreadySetError := strings.HasPrefix(err.Error(), "110043")
		if ignoreAlreadySetError && isAlreadySetError {
			return nil
		}
	}

	return err
}

func (byb *bybitNormalised) placeOrder(
	symbol string,
	side string,
	qty Decimal,
	reduceOnly bool,
	tp *Decimal,
	sl *Decimal,
) []OrderRes {
	// get the qty list
	qtyList := getBulkQtyList(qty, symbol, cls.Bybit)

	// create chan for order results to return to
	numOrders := len(qtyList)
	resChan := make(chan OrderRes, numOrders)

	// create vars to take pointers of
	isLev := bybit.IsLeverageTrue
	posIdx := bybit.PositionIdxOneWay

	tpStr := ""
	if tp != nil {
		tpStr = fmt.Sprint(*tp)
	}

	slStr := ""
	if sl != nil {
		slStr = fmt.Sprint(*sl)
	}

	// send bybit orders
	orderStart := time.Now().In(time.UTC)
	for _, qty := range qtyList {
		go func(qty Decimal) {
			// create request
			req := bybit.V5CreateOrderParam{
				Category:       bybit.CategoryV5Linear,
				Symbol:         bybit.SymbolV5(symbol),
				Side:           bybit.Side(side),
				OrderType:      bybit.OrderTypeMarket,
				Qty:            qty.String(),
				IsLeverage:     &isLev,
				PositionIdx:    &posIdx,
				TakeProfit:     &tpStr,
				StopLoss:       &slStr,
				ReduceOnly:     &reduceOnly,
				CloseOnTrigger: &reduceOnly,
			}

			// make request
			byb.setNextClient()
			_, err := byb.cli.V5().Order().CreateOrder(req)

			// send response to the collection chan
			normalisedReq := castBybitParamToNormal(req)
			if err != nil {
				resChan <- OrderRes{qty: qty, resStr: err.Error(), req: normalisedReq, orderStart: orderStart}
			} else {
				resChan <- OrderRes{qty: qty, resStr: "200", req: normalisedReq, orderStart: orderStart}
			}
		}(qty)
	}

	// return order result
	results := make([]OrderRes, numOrders)
	for i := 0; i <= numOrders-1; i++ {
		results[i] = <-resChan
	}
	return results
}

func (byb *bybitNormalised) getPrice(symbol string) (Decimal, bool, error) {
	// create param
	reqSymbol := bybit.SymbolV5(symbol)
	reqParam := bybit.V5GetTickersParam{
		Category: bybit.CategoryV5Linear,
		Symbol:   &reqSymbol,
		BaseCoin: nil,
		ExpDate:  nil,
	}

	// send request
	byb.setNextClient()
	res, err := byb.cli.V5().Market().GetTickers(reqParam)

	if err != nil {
		if err.Error() == "10001, params error: symbol invalid" {
			return Decimal{}, false, nil
		}
		return Decimal{}, false, fmt.Errorf("err making api request, %v", err)
	}

	priceList := res.Result.LinearInverse.List
	numPrices := len(priceList)
	if numPrices != 1 {
		return Decimal{}, false, fmt.Errorf(
			"expected 1 result, got %d, res: (%v)", numPrices, res)
	}

	price, err := decimal.NewFromString(priceList[0].LastPrice)
	if err != nil {
		return Decimal{}, true, fmt.Errorf("failed to parse lastPrice as Decimal, %v", err)
	}
	return price, true, nil
}

func (byb *bybitNormalised) getSymbolData() ([]Symbol, error) {
	// create params
	// coinUSDT := bybit.CoinUSDT
	reqParam := bybit.V5GetInstrumentsInfoParam{
		Category: bybit.CategoryV5Linear,
		Symbol:   nil,
		BaseCoin: nil,
		Limit:    nil,
		Cursor:   nil,
	}

	// make request
	byb.setNextClient()
	res, err := byb.cli.V5().Market().GetInstrumentsInfo(reqParam)
	if err != nil {
		return nil, fmt.Errorf("failed to send request, %v, res: %v",
			err, res)
	}

	// iterate over the symbols in symbolData.Result
	var symbols []cls.Symbol
	for _, bybSymbol := range res.Result.LinearInverse.List {
		// cast fields to decimals
		maxLeverage, err := decimal.NewFromString(bybSymbol.LeverageFilter.MaxLeverage)
		if err != nil {
			return nil, fmt.Errorf("failed to cast maxLeverage to decimal, %v", err)
		}
		qtyStep, err := decimal.NewFromString(bybSymbol.LotSizeFilter.QtyStep)
		if err != nil {
			return nil, fmt.Errorf("failed to cast qtyStep to decimal, %v", err)
		}
		minQty, err := decimal.NewFromString(bybSymbol.LotSizeFilter.MinOrderQty)
		if err != nil {
			return nil, fmt.Errorf("failed to cast minQty to decimal, %v", err)
		}
		maxQty, err := decimal.NewFromString(bybSymbol.LotSizeFilter.MaxOrderQty)
		if err != nil {
			return nil, fmt.Errorf("failed to cast maxQty to decimal, %v", err)
		}
		priceStep, err := decimal.NewFromString(bybSymbol.PriceFilter.TickSize)
		if err != nil {
			return nil, fmt.Errorf("failed to cast priceStep to decimal, %v", err)
		}

		symbol := cls.Symbol{
			Name:        string(bybSymbol.Symbol),
			MaxLeverage: maxLeverage,
			QtyStep:     qtyStep,
			MinQty:      minQty,
			MaxQty:      maxQty,
			PriceStep:   priceStep,
		}

		symbols = append(symbols, symbol)
	}

	return symbols, nil
}

func (byb *bybitNormalised) setNextClient() {
	byb.cli.WithHTTPClient(handler.excData[cls.Bybit].NextCli())
}

func castBybitParamToNormal(p bybit.V5CreateOrderParam) CreateOrderParam {
	// cast stuff to decimals.
	var err error

	price := decimal.Zero
	if p.Price != nil && *p.Price != "" {
		price, err = decimal.NewFromString(*p.Price)
		if err != nil {
			log.Panicf("failed to cast price to decimal, price: %v, err: %v", *p.Price, err)
		}
	}

	tp := decimal.Zero
	if p.TakeProfit != nil && *p.TakeProfit != "" {
		tp, err = decimal.NewFromString(*p.TakeProfit)
		if err != nil {
			log.Panicf("failed to cast tp to decimal, price: %v, err: %v", *p.TakeProfit, err)
		}
	}

	sl := decimal.Zero
	if p.StopLoss != nil && *p.StopLoss != "" {
		sl, err = decimal.NewFromString(*p.StopLoss)
		if err != nil {
			log.Panicf("failed to cast sl to decimal, price: %v, err: %v", *p.StopLoss, err)
		}
	}

	timeInForce := bybit.TimeInForceGoodTillCancel
	if p.TimeInForce != nil {
		timeInForce = *p.TimeInForce
	}

	posIdx := int(*p.PositionIdx) // dont need to check != nil, positionIdx is required param

	return CreateOrderParam{
		Side:           string(p.Side),
		Symbol:         string(p.Symbol),
		OrderType:      string(p.OrderType),
		Qty:            p.Qty,
		TimeInForce:    string(timeInForce),
		ReduceOnly:     *p.ReduceOnly,
		CloseOnTrigger: *p.CloseOnTrigger,
		Price:          &price,
		TakeProfit:     &tp,
		StopLoss:       &sl,
		PositionIdx:    &posIdx,
	}
}

func (byb *bybitNormalised) setAccountType() {
	if byb.accountType == nil {
		assumedAcctType := bybit.AccountTypeUnified // assume that its unified if can't get it

		// get account type
		info, err := byb.cli.V5().Account().GetAccountInfo()
		if err != nil {
			common.LogAndSendAlertF("failed to get account type (assuming unified), %v", err)

			byb.accountType = &assumedAcctType
			return
		}

		// determine account type
		acctTypeInt := info.Result.UnifiedMarginStatus

		acctType := bybit.AccountTypeNormal
		if acctTypeInt != 1 {
			acctType = bybit.AccountTypeUnified
		}
		log.Printf("found account type: %s", string(acctType))

		byb.accountType = &acctType
	}
}

func botPosFromBybPos(
	pos bybit.V5GetPositionInfoItem,
) (cls.BotPosition, error) {
	empty := cls.BotPosition{}

	// parse strings as decimals
	qty, err := decimal.NewFromString(pos.Size)
	if err != nil {
		return empty, fmt.Errorf("failed to parse qty %s as Decimal, %v", pos.Size, err)
	}
	entry, err := decimal.NewFromString(pos.AvgPrice)
	if err != nil {
		return empty, fmt.Errorf("failed to parse entry %s as Decimal, %v", pos.AvgPrice, err)
	}
	lev, err := decimal.NewFromString(pos.Leverage)
	if err != nil {
		return empty, fmt.Errorf("failed to parse leverage %s as Decimal, %v", pos.Leverage, err)
	}
	tp, err := decimal.NewFromString(pos.TakeProfit)
	if err != nil {
		return empty, fmt.Errorf("failed to parse tp %s as Decimal, %v", pos.TakeProfit, err)
	}
	sl, err := decimal.NewFromString(pos.StopLoss)
	if err != nil {
		return empty, fmt.Errorf("failed to parse sl %s as Decimal, %v", pos.StopLoss, err)
	}

	// add to position map
	botPos := cls.BotPosition{
		Symbol:          string(pos.Symbol),
		Side:            string(pos.Side),
		Qty:             qty,
		Entry:           entry,
		Leverage:        lev,
		IgnoreAddsUntil: time.Now(),
		Tp:              tp,
		Sl:              sl,
		Exchange:        "",
	}

	return botPos, nil
}

type phemexNormalised struct {
	cli *phemex.Client
}

func (ph *phemexNormalised) getClosedPNL(symbol string, closedAfter *int64) ([]ClosedPNL, error) {
	panic("unimplemented")
}

func (ph *phemexNormalised) getMyPosition(symbol string) (cls.BotPosition, error) {
	panic("unimplemented")
}

func (ph *phemexNormalised) getMyPositions() (map[string]cls.BotPosition, error) {
	panic("unimplemented")
}

func (ph *phemexNormalised) getPrice(symbol string) (Decimal, bool, error) {
	panic("unimplemented")
}

func (ph *phemexNormalised) getSymbolData() ([]cls.Symbol, error) {
	panic("unimplemented")
}

func (ph *phemexNormalised) getUSDTBalance() (Decimal, error) {
	panic("unimplemented")
}

func (ph *phemexNormalised) placeOrder(symbol string, side string, qty Decimal, reduceOnly bool, tp *Decimal, sl *Decimal) []OrderRes {
	panic("unimplemented")
}

func (ph *phemexNormalised) setLeverage(
	symbol string,
	leverage Decimal,
	ignoreAlreadySetError bool,
) error {
	panic("unimplemented")
}

func (ph *phemexNormalised) setNextClient() {
	panic("unimplemented")
}
