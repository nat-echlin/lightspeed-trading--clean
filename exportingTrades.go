package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lightspeed-trading/app/common"
	"github.com/lightspeed-trading/app/db"
	"github.com/tealeg/xlsx"
)

func launchTradesExporter(
	fnameChan chan<- string,
	pool *pgxpool.Pool,
) {
	log.Printf("tradesExporter : launched trades exporter. ")

	for {
		nextRunTime := getNextRunTime()
		durationUntilNextRun := time.Until(nextRunTime)

		timer := time.NewTimer(durationUntilNextRun)
		<-timer.C

		log.Printf("tradesExporter : exporting trades…")

		filename, err := prepForExport(pool)
		if err != nil {
			common.LogAndSendAlertF(
				"tradesExporter : failed to prep trades for export, %v", err,
			)
			continue
		}

		log.Printf("tradesExporter : sending %s to discord bot", filename)
		fnameChan <- filename
	}
}

// create and save a file containing all the trades in the "trades" psql
// table. The returned string is the filename of where the file was
// saved
func prepForExport(pool *pgxpool.Pool) (string, error) {
	file := xlsx.NewFile()

	// create a sheet for PnLs
	pnlSheet, err := file.AddSheet("PnLs")
	if err != nil {
		return "", fmt.Errorf("failed to add pnls sheet, %v", err)
	}
	pnlHeaderRow := pnlSheet.AddRow()
	pnlHeaderRow.AddCell().SetString("TraderUID")
	pnlHeaderRow.AddCell().SetString("TotalPnL")

	trades, err := fetchTrades(pool)
	if err != nil {
		return "", fmt.Errorf("failed to fetch trades, %v", err)
	}

	// map to hold PnLs for each trader
	pnlMap := make(map[string]float64)

	for _, trade := range trades {
		// check if a sheet for this trader exists
		sheet, ok := file.Sheet[trade.TraderUID]
		if !ok {
			// create sheet for this trader
			sheet, err = file.AddSheet(trade.TraderUID)
			if err != nil {
				return "", fmt.Errorf("failed to add sheet, %v", err)
			}
			// add column headers
			headerRow := sheet.AddRow()
			headerRow.AddCell().SetString("TradeType")
			headerRow.AddCell().SetString("Symbol")
			headerRow.AddCell().SetString("Side")
			headerRow.AddCell().SetString("Qty")
			headerRow.AddCell().SetString("EntryPrice")
			headerRow.AddCell().SetString("MarketPrice")
			headerRow.AddCell().SetString("PnL")
			headerRow.AddCell().SetString("Timestamp")
		}

		// add this trade to the trader's sheet
		row := sheet.AddRow()
		row.AddCell().SetString(trade.TradeType)
		row.AddCell().SetString(trade.Symbol)
		row.AddCell().SetString(trade.Side)
		row.AddCell().SetFloat(trade.Qty)
		row.AddCell().SetFloat(trade.EntryPrice.InexactFloat64())
		row.AddCell().SetFloat(trade.MarketPrice)
		row.AddCell().SetFloat(trade.Pnl)
		row.AddCell().SetString(trade.UtcTimestamp.Format(time.RFC3339))

		// update PnL for this trader
		pnlMap[trade.TraderUID] += trade.Pnl
	}

	// populate PnLs sheet
	for trader, pnl := range pnlMap {
		row := pnlSheet.AddRow()
		row.AddCell().SetString(trader)
		row.AddCell().SetFloat(pnl)
	}

	// save the xlsx file
	currentDate := time.Now().UTC().Format("2January") // "2" for day without leading zero, "January" for full month
	filename := "trades_" + currentDate + ".xlsx"
	err = file.Save(filename)
	if err != nil {
		return "", fmt.Errorf("failed to save file: %v", err)
	}

	log.Printf(
		"tradesExporter : successfully exported (%d) trades into %s",
		len(trades), filename,
	)

	return filename, nil
}

func fetchTrades(pool *pgxpool.Pool) ([]db.Trade, error) {
	// read from psql
	rows, err := pool.Query(
		context.Background(),
		"SELECT trader_uid, trade_type, symbol, side, qty, utc_timestamp, entry_price, market_price, pnl FROM trades",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to read trades table, %v", err)
	}
	defer rows.Close()

	// parse into a []Trade
	var trades []db.Trade
	for rows.Next() {
		var trade db.Trade
		err := rows.Scan(
			&trade.TraderUID, &trade.TradeType, &trade.Symbol, &trade.Side,
			&trade.Qty, &trade.UtcTimestamp, &trade.EntryPrice,
			&trade.MarketPrice, &trade.Pnl,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to parse rows, %v", err)
		}
		trades = append(trades, trade)
	}

	return trades, nil
}

// getNextRunTime calculates the next time the exporter should run.
// For example, to run every Sunday at 4 PM.
func getNextRunTime() time.Time {
	now := time.Now()

	// find the next Monday - mondays at 8pm is a time we can consistently be around on
	nextRunTime := time.Date(
		now.Year(), now.Month(), now.Day(),
		20, 0, 0, 0, time.UTC,
	)
	for nextRunTime.Weekday() != time.Monday {
		nextRunTime = nextRunTime.Add(24 * time.Hour)
	}

	// if we've already passed the run time for this week, schedule for next week.
	if nextRunTime.Before(now) {
		nextRunTime = nextRunTime.Add(7 * 24 * time.Hour)
	}

	return nextRunTime
}
