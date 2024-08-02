package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	pgxdecimal "github.com/jackc/pgx-shopspring-decimal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

var ErrNoUser = errors.New("user doesn't exist")

func GetConnPool(dbName string, dbUser string, dbPass string, testing bool) (*pgxpool.Pool, error) {
	// establish db name
	if testing {
		dbName += "_dev"
	}

	// create pool config
	connectionString := fmt.Sprintf(
		"postgresql://%s:%s@localhost:5432/%s",
		dbUser, dbPass, dbName,
	)
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("couldn't parse pool config: %v", err)
	}

	// Set AfterConnect hook to register decimal type
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		pgxdecimal.Register(conn.TypeMap())
		return nil
	}

	// create pool with the config
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("couldn't connect to db: %v", err)
	}

	return pool, nil
}

// get the row of a bot in the copytrading table CPYT
func ReadRecordCPYT(pool *pgxpool.Pool, userID string, botID int) (BotConfig, error) {
	var botData BotConfig

	// populate BotData variable
	row := pool.QueryRow(context.Background(), "SELECT * FROM copytrading WHERE user_id=$1 AND bot_id=$2", userID, botID)
	err := row.Scan(&botData.UserID, &botData.BotID, &botData.BotName, &botData.BotStatus, &botData.APIKeyRaw, &botData.APISecretRaw,
		&botData.Exchange, &botData.BinanceID, &botData.ExcSpreadDiffPercent, &botData.Leverage, &botData.InitialOpenPercent,
		&botData.MaxAddMultiplier, &botData.OpenDelay, &botData.AddDelay, &botData.OneCoinMaxPercent, &botData.BlacklistCoins, &botData.WhitelistCoins,
		&botData.AddPreventionPercent, &botData.BlockAddsAboveEntry, &botData.MaxOpenPositions, &botData.AutoTP, &botData.AutoSL,
		&botData.MinTraderSize, &botData.TestMode, &botData.NotifyDest, &botData.ModeCloseOnly, &botData.ModeInverse,
		&botData.ModeNoClose, &botData.ModeBTCTriggerPrice, &botData.ModeNoSells)

	if err != nil {
		return botData, fmt.Errorf("failed to scan row, %v", err)
	}

	return botData, nil
}

// delete all records of a user from all tables
func WipeUser(
	pool *pgxpool.Pool,
	userID string,
	wipeUsers bool,
	wipeCpyt bool,
) error {
	// acquire conn from pool
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		return fmt.Errorf("failed to acquire conn, %v", err)
	}

	// delete from copytrading
	if wipeCpyt {
		_, err = conn.Exec(
			context.Background(),
			"DELETE FROM copytrading WHERE user_id = $1;",
			userID,
		)
		if err != nil {
			return fmt.Errorf("failed to delete from copytrading, %v", err)
		}
	}

	// delete from users
	if wipeUsers {
		_, err = conn.Exec(
			context.Background(),
			"DELETE FROM users WHERE user_id = $1;",
			userID,
		)
		if err != nil {
			return fmt.Errorf("failed to delete from users, %v", err)
		}
	}
	return nil
}

type BotStatus string

const (
	BotActive   BotStatus = "active"
	BotInactive BotStatus = "inactive"
	BotStarting BotStatus = "starting"
	BotStopping BotStatus = "stopping"
)

// sets the bot_status for a bot
func SetBotActivity(userID string, botID int, status BotStatus, pool *pgxpool.Pool) error {
	// execute SQL statement
	_, err := pool.Exec(
		context.Background(),
		`UPDATE copytrading SET bot_status = $1 WHERE user_id = $2 AND bot_id = $3`,
		status,
		userID,
		botID,
	)

	if err != nil {
		return fmt.Errorf("failed to update bot activity: %w", err)
	}

	return nil
}

// Resets all bots who are in starting or stopping mode to inactive.
// Returns the number of bots it reset.
func ResetStartingOrStoppingToInactive(pool *pgxpool.Pool) (int, error) {
	query := `
		UPDATE copytrading
		SET bot_status = 'inactive'
		WHERE bot_status IN ('starting', 'stopping')
	`
	tag, err := pool.Exec(context.Background(), query)
	if err != nil {
		return 0, err
	}

	return int(tag.RowsAffected()), nil
}

// write to copytrading table. Used for initial insertion, not for
// updating an existing record.
func WriteRecordCPYT(pool *pgxpool.Pool, botData BotConfig) error {
	query := `INSERT INTO copytrading (
		user_id, bot_id, bot_name, bot_status, api_key_raw, api_secret_raw,
		exchange, binance_id, exc_spread_diff_percent, leverage, initial_open_percent, max_add_multiplier,
		open_delay, add_delay, one_coin_max_percent, blacklist_coins, whitelist_coins, add_prevention_percent,
		block_adds_above_entry, max_open_positions, auto_tp, auto_sl, min_trader_size, test_mode,
		notify_dest, mode_close_only, mode_inverse, mode_no_close, mode_btc_trigger_price, mode_no_sells
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, 
		$25, $26, $27, $28, $29, $30
	)`
	_, err := pool.Exec(context.Background(), query,
		botData.UserID, botData.BotID, botData.BotName, botData.BotStatus, botData.APIKeyRaw, botData.APISecretRaw,
		botData.Exchange, botData.BinanceID, botData.ExcSpreadDiffPercent, botData.Leverage, botData.InitialOpenPercent,
		botData.MaxAddMultiplier, botData.OpenDelay, botData.AddDelay, botData.OneCoinMaxPercent, botData.BlacklistCoins, botData.WhitelistCoins,
		botData.AddPreventionPercent, botData.BlockAddsAboveEntry, botData.MaxOpenPositions, botData.AutoTP, botData.AutoSL,
		botData.MinTraderSize, botData.TestMode, botData.NotifyDest, botData.ModeCloseOnly, botData.ModeInverse,
		botData.ModeNoClose, botData.ModeBTCTriggerPrice, botData.ModeNoSells)
	if err != nil {
		return fmt.Errorf("failed to execute query, %v", err)
	}
	return nil
}

// get all rows for a user in the copytrading table
func ReadAllRecordsCPYT(pool *pgxpool.Pool, userID string) ([]BotConfig, error) {
	var botConfigs []BotConfig

	// query all rows for the given user
	rows, err := pool.Query(context.Background(), "SELECT * FROM copytrading WHERE user_id=$1", userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query rows, %v", err)
	}
	defer rows.Close()

	// iterate through the rows
	for rows.Next() {
		var botData BotConfig
		err := rows.Scan(&botData.UserID, &botData.BotID, &botData.BotName, &botData.BotStatus, &botData.APIKeyRaw, &botData.APISecretRaw,
			&botData.Exchange, &botData.BinanceID, &botData.ExcSpreadDiffPercent, &botData.Leverage, &botData.InitialOpenPercent,
			&botData.MaxAddMultiplier, &botData.OpenDelay, &botData.AddDelay, &botData.OneCoinMaxPercent, &botData.BlacklistCoins, &botData.WhitelistCoins,
			&botData.AddPreventionPercent, &botData.BlockAddsAboveEntry, &botData.MaxOpenPositions, &botData.AutoTP, &botData.AutoSL,
			&botData.MinTraderSize, &botData.TestMode, &botData.NotifyDest, &botData.ModeCloseOnly, &botData.ModeInverse,
			&botData.ModeNoClose, &botData.ModeBTCTriggerPrice, &botData.ModeNoSells)

		if err != nil {
			return nil, fmt.Errorf("failed to scan row, %v", err)
		}

		botConfigs = append(botConfigs, botData)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed during row iteration, %v", err)
	}

	return botConfigs, nil
}

// reduce a users remaining copytrading balance by the given amount, but
// only if they have enough remaining balance to do so. Return true if
// it was a success (they have enough), false if it was a failure (they
// did not have enough balance). If false and no err, it will return the
// remaining balance as well.
func CheckAndReduceCpytBalance(
	userID string,
	requestedBal decimal.Decimal,
	pool *pgxpool.Pool,
) (bool, decimal.Decimal, error) {
	// do sql command
	tag, err := pool.Exec(
		context.Background(),
		`
			UPDATE users
			SET remaining_cpyt_bal = remaining_cpyt_bal - $1
			WHERE user_id = $2 AND remaining_cpyt_bal >= $1
		`,
		requestedBal, userID,
	)
	if err != nil {
		return false, decimal.Zero, err
	}

	success := tag.RowsAffected() == 1 // if it did anything or not
	if success {
		return true, decimal.Zero, nil
	}

	// not enough bal - find the remaining balance
	var remainingBalance decimal.Decimal
	err = pool.QueryRow(
		context.Background(),
		"SELECT remaining_cpyt_bal FROM users WHERE user_id = $1",
		userID,
	).Scan(&remainingBalance)

	if err != nil {
		return false, decimal.Zero, err
	}
	return false, remainingBalance, nil
}

// add back to a users copytrading balance (presumabley after theyv stopped
// their bot)
func AddBackToCpytBal(userID string, returnedBal decimal.Decimal, pool *pgxpool.Pool) error {
	// Update the user's balance by adding the returnedBal
	tag, err := pool.Exec(
		context.Background(),
		`
			UPDATE users
			SET remaining_cpyt_bal = remaining_cpyt_bal + $1
			WHERE user_id = $2
		`,
		returnedBal, userID,
	)
	if tag.RowsAffected() == 0 {
		return ErrNoUser
	}
	return err
}

// add to a user's renewal time by the given interval
// the interval string must be a valid psql interval
func AddToUserRenewalDate(userID string, interval string, pool *pgxpool.Pool) error {
	tag, err := pool.Exec(
		context.Background(),
		`
		UPDATE users
		SET renewal_ts = renewal_ts + $2::INTERVAL
		WHERE user_id = $1
		`,
		userID, interval,
	)
	if tag.RowsAffected() == 0 {
		return ErrNoUser
	}
	return err
}

func UpdateUserAlertWebhook(
	userID string,
	newHook string,
	pool *pgxpool.Pool,
) error {
	tag, err := pool.Exec(
		context.Background(),
		`
		UPDATE users
		SET alert_webhook = $2
		WHERE user_id = $1
		`,
		userID, newHook,
	)

	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoUser
	}

	return nil
}

func WriteRecordUsers(user User, pool *pgxpool.Pool) error {
	user.RenewalTS = user.RenewalTS.In(time.UTC)
	_, err := pool.Exec(
		context.Background(),
		`INSERT INTO users (user_id, plan, renewal_ts, alert_webhook, remaining_cpyt_bal) VALUES ($1, $2, $3, $4, $5)`,
		user.UserID, user.Plan, user.RenewalTS, user.AlertWebhook, user.RemainingCpytBal,
	)

	return err
}

func ReadRecordUsers(userID string, pool *pgxpool.Pool) (User, error) {
	var user User

	// read from db
	row := pool.QueryRow(
		context.Background(),
		"SELECT * FROM users WHERE user_id=$1",
		userID,
	)

	err := row.Scan(&user.UserID, &user.Plan, &user.RenewalTS, &user.AlertWebhook, &user.RemainingCpytBal)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return user, ErrNoUser
		}
		return user, fmt.Errorf("failed to scan row, %v", err)
	}

	return user, nil
}

func ParseBotStatus(s string) (BotStatus, error) {
	switch s {
	case string(BotActive):
		return BotActive, nil
	case string(BotInactive):
		return BotInactive, nil
	case string(BotStarting):
		return BotStarting, nil
	case string(BotStopping):
		return BotStopping, nil
	}
	return "", fmt.Errorf("invalid BotStatus value %s", s)
}
