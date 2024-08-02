package cls

import (
	"encoding/json"
	"fmt"
)

// both bin quantities are given as absolute values, and side is one of "Buy", "Sell".
// Type is one of OpenLS, CloseLS, ChangeLS
// Pnl and MarkPrice have `json:"-"`, which tells the json marshaller to not include
// them when it marshalls jsons for sending to users, because we don't need users to
// receive that data. Change this if necessary for a new feature.
type Update struct {
	Type        string  `json:"type"`
	Symbol      string  `json:"coin"`
	Side        string  `json:"side"`
	BIN_qty     float64 `json:"BIN_qty"`
	Timestamp   float64 `json:"timestamp"`
	Old_BIN_qty float64 `json:"BIN_qty_old"`
	Entry       Decimal `json:"entry"`
	Pnl         float64 `json:"-"`
	MarkPrice   float64 `json:"-"`
}

// update + all data needed for a Bot to make a trading decision (for Opens)
type OpenUpdate struct {
	Upd                  Update
	SymbolPrices         map[string]Price // string is an exchange name
	TraderSize           Decimal
	ExcSpreadDiffPercent SpreadDiffs
}

// update + data needed when changing a position
type ChangeUpdate struct {
	Upd          Update
	SymbolPrices map[string]Price
	IsIncrease   bool
}

// contains the price for multiple exchanges
type PricesRes struct {
	Prices   map[string]Price // exchange name : Price
	PanicErr error
}

type Price struct {
	ExcName     string
	Market      Decimal
	Err         error
	IsSupported bool
}

// contains the spread diff % against binance for multiple exchanges
type SpreadDiffs = map[string]Decimal

var (
	OpenPing     = "Position Opened"
	ClosePing    = "Position Closed"
	IncreasePing = "Position Added To"
	DecreasePing = "Position Partially Sold"
)

var (
	OpenLS   = "OPEN"
	CloseLS  = "CLOSE"
	ChangeLS = "CHANGE"
)

// returns with LONG or SHORT in front of the coin name, and it has double
// asterisks around the long / short to embolden it in discord
func (upd *Update) FmtCoin() string {
	var fmtCoin string
	switch upd.Side {
	case "Buy":
		fmtCoin = "**LONG** " + upd.Symbol
	case "Sell":
		fmtCoin = "**SHORT** " + upd.Symbol
	default:
		fmtCoin = upd.Symbol
	}

	return fmtCoin
}

// switch the side (for inverse modes)
func (upd *Update) SwitchSide() {
	if upd.Side == "Buy" {
		upd.Side = "Sell"
	} else if upd.Side == "Sell" {
		upd.Side = "Buy"
	} else {
		panic(fmt.Sprintf("bad side given: %s", upd.Side))
	}
}

// switch side
func SwitchSide(side string) string {
	if side == "Buy" {
		return "Sell"
	} else {
		return "Buy"
	}
}

// get the colour for a discord ping
func (upd *Update) FmtTitleColour() (string, int) {
	var title string
	var colour int

	switch upd.Type {
	case OpenLS:
		title = OpenPing
		colour = 16777215
	case CloseLS:
		title = ClosePing
		colour = 2303786
	case ChangeLS:
		if upd.BIN_qty > upd.Old_BIN_qty {
			title = IncreasePing
			colour = 5763719
		} else {
			title = DecreasePing
			colour = 15548997
		}
	}

	return title, colour
}

func (upd *Update) FmtAmount(pingType string) string {
	if pingType == OpenPing {
		return FmtFloat(upd.BIN_qty)

	} else if pingType == ClosePing {
		return FmtFloat(upd.Old_BIN_qty)

	} else if pingType == IncreasePing {
		return fmt.Sprintf("%s (added to %s)", FmtFloat(upd.BIN_qty-upd.Old_BIN_qty), FmtFloat(upd.Old_BIN_qty))

	} else if pingType == DecreasePing {
		return fmt.Sprintf("%s (of %s)", FmtFloat(upd.Old_BIN_qty-upd.BIN_qty), FmtFloat(upd.Old_BIN_qty))
	}

	return FmtFloat(upd.BIN_qty)
}

func (u Update) String() string {
	switch u.Type {
	case OpenLS:
		return fmt.Sprintf(
			"Update{%s %s %s, BinQty: %s, Timestamp: %.0f, Entry: %s}",
			OpenLS, u.Side, u.Symbol, FmtFloat(u.BIN_qty), u.Timestamp, u.Entry,
		)
	case CloseLS, ChangeLS:
		return fmt.Sprintf(
			"Update{%s %s %s, BinQty: %s, BinQtyOld: %s, Timestamp: %.0f, Entry: %s, MarkPrice: %s}",
			u.Type, u.Side, u.Symbol, FmtFloat(u.BIN_qty), FmtFloat(u.Old_BIN_qty), u.Timestamp, u.Entry, FmtFloat(u.MarkPrice),
		)
	default:
		return fmt.Sprintf(
			"Update{%s, Symbol: %s, Side: %s, BIN_qty: %s, Timestamp: %.0f, Old_BIN_qty: %s, Entry: %s, Pnl: %s, MarkPrice: %s}",
			u.Type, u.Symbol, u.Side, FmtFloat(u.BIN_qty), u.Timestamp, FmtFloat(u.Old_BIN_qty), u.Entry, FmtFloat(u.Pnl), FmtFloat(u.MarkPrice),
		)
	}
}

func (openUpdate OpenUpdate) String() string {
	// Use json.Marshal to create string representation of maps, with error checking
	coinPriceBytes, err := json.Marshal(openUpdate.SymbolPrices)
	if err != nil {
		coinPriceBytes = []byte("error marshaling CoinPrice")
	}

	spreadDiffBytes, err := json.Marshal(openUpdate.ExcSpreadDiffPercent)
	if err != nil {
		spreadDiffBytes = []byte("error marshaling ExcSpreadDiffPercent")
	}

	return fmt.Sprintf("OpenUpdate: {Upd: %s, CoinPrice: %s, TraderSize: %s, ExcSpreadDiffPercent: %s}",
		openUpdate.Upd, string(coinPriceBytes), openUpdate.TraderSize, string(spreadDiffBytes))
}

// return if an update is an increase or not
func (upd Update) IsIncrease() bool {
	switch upd.Type {
	case ChangeLS:
		if upd.BIN_qty > upd.Old_BIN_qty {
			return true
		} else {
			return false
		}
	case CloseLS:
		return false
	case OpenLS:
		return true
	default:
		return false // should never occur
	}
}

func (p Price) String() string {
	if p.Err != nil {
		return fmt.Sprintf("Price{ExcName: %s, Error: %s}", p.ExcName, p.Err.Error())
	}

	if !p.IsSupported {
		return fmt.Sprintf("Price{ExcName: %s, not supported}", p.ExcName)
	}

	return fmt.Sprintf("Price{ExcName: %s, Market: %s}", p.ExcName, p.Market)
}
