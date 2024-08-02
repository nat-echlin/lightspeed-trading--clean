package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type Trade struct {
	TraderUID    string          `json:"trader_uid"`
	TradeType    string          `json:"trade_type"`
	Symbol       string          `json:"symbol"`
	Side         string          `json:"side"`
	Qty          float64         `json:"qty"`
	UtcTimestamp time.Time       `json:"utc_timestamp"`
	EntryPrice   decimal.Decimal `json:"entry"`
	MarketPrice  float64         `json:"mark"`
	Pnl          float64         `json:"pnl"`
}

// writes a trade to the psql database
func (tr *Trade) Write(pool *pgxpool.Pool) error {
	// execute the INSERT statement
	_, err := pool.Exec(
		context.Background(),
		`INSERT INTO trades (trader_uid, trade_type, symbol, side, qty, utc_timestamp, entry_price, market_price, pnl) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		tr.TraderUID, tr.TradeType, tr.Symbol, tr.Side, tr.Qty, tr.UtcTimestamp, tr.EntryPrice, tr.MarketPrice, tr.Pnl,
	)

	// return any errors
	return err
}

func (tr *Trade) String() string {
	return fmt.Sprintf(
		"TraderUID: %s, TradeType: %s, Symbol: %s, Side: %s, Qty: %s, UtcTimestamp: %d, EntryPrice: %s, MarketPrice: %s, Pnl: %s",
		tr.TraderUID, tr.TradeType, tr.Symbol, tr.Side, FmtFloat(tr.Qty),
		tr.UtcTimestamp.UTC().Unix(), tr.EntryPrice,
		FmtFloat(tr.MarketPrice), FmtFloat(tr.Pnl),
	)
}

// fmtFloat formats a float64 to remove unnecessary trailing zeros
func FmtFloat(fl float64) string {
	// convert float to string with sufficient precision
	str := fmt.Sprintf("%.8f", fl)

	str = strings.TrimRight(str, "0")
	str = strings.TrimSuffix(str, ".")

	return str
}
