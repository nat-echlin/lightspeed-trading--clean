package main

import (
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	"github.com/lightspeed-trading/app/db"
)

// create a Trade from an update and trader name
func newTradeFromUpdate(upd cls.Update, traderUID string) db.Trade {
	// work out trade type, trade qty
	var tradeType string
	var qty float64

	switch upd.Type {
	case cls.OpenLS:
		tradeType = "Open"
		qty = upd.BIN_qty
	case cls.CloseLS:
		tradeType = "Close"
		qty = upd.Old_BIN_qty
	case cls.ChangeLS:
		if upd.BIN_qty > upd.Old_BIN_qty {
			tradeType = "Increase"
			qty = upd.BIN_qty - upd.Old_BIN_qty
		} else {
			tradeType = "Decrease"
			qty = upd.Old_BIN_qty - upd.BIN_qty
		}
	}

	// create & return the Trade
	unixSeconds := int64(upd.Timestamp)
	tradeTime := time.Unix(unixSeconds, 0)
	return db.Trade{
		TraderUID:    traderUID,
		TradeType:    tradeType,
		Symbol:       upd.Symbol,
		Side:         upd.Side,
		Qty:          qty,
		UtcTimestamp: tradeTime,
		EntryPrice:   upd.Entry,
		MarketPrice:  upd.MarkPrice,
		Pnl:          upd.Pnl,
	}
}

// store a list of updates, will log errors and not propagate them
// storeTrades will only store updates that are sells, be it a decrease
// of a position or an entire sell
func storeTrades(
	updates []cls.Update,
	traderUID string,
	pool *pgxpool.Pool,
) {
	// sort out the updates so we only have sells
	usefulUpdates := []cls.Update{}
	for _, upd := range updates {
		if !upd.IsIncrease() {
			usefulUpdates = append(usefulUpdates, upd)
		}
	}

	// insert into the db
	for _, upd := range usefulUpdates {
		trade := newTradeFromUpdate(upd, traderUID)

		err := trade.Write(pool)
		if err != nil {
			common.LogAndSendAlertF(
				"%s : failed to store trade in trades table, %v",
				traderUID, err,
			)
		} else {
			log.Printf(
				"%s : success storing %s in trades table",
				traderUID, upd.Symbol,
			)
		}
	}
}
