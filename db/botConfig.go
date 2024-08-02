package db

import (
	"fmt"
	"reflect"

	"github.com/shopspring/decimal"
)

type BotConfig struct {
	UserID               string          `json:"userID"`
	BotID                int             `json:"botID"`
	BotName              string          `json:"botName"`
	BotStatus            BotStatus       `json:"botStatus"`
	APIKeyRaw            []byte          `json:"apiKeyRaw"`
	APISecretRaw         []byte          `json:"apiSecretRaw"`
	Exchange             string          `json:"exchange"`
	BinanceID            string          `json:"binanceID"`
	ExcSpreadDiffPercent decimal.Decimal `json:"excSpreadDiffPercent"` // nil value: -1
	Leverage             decimal.Decimal `json:"leverage"`
	InitialOpenPercent   decimal.Decimal `json:"initialOpenPercent"`
	MaxAddMultiplier     decimal.Decimal `json:"maxAddMultiplier"`  // nil value: -1
	OpenDelay            decimal.Decimal `json:"openDelay"`         // nil value: -1
	AddDelay             decimal.Decimal `json:"addDelay"`          // nil value: -1
	OneCoinMaxPercent    decimal.Decimal `json:"oneCoinMaxPercent"` // nil value: -1
	BlacklistCoins       []string        `json:"blacklistCoins"`
	WhitelistCoins       []string        `json:"whitelistCoins"`
	AddPreventionPercent decimal.Decimal `json:"addPreventionPercent"` // nil value: -1
	BlockAddsAboveEntry  bool            `json:"blockAddsAboveEntry"`
	MaxOpenPositions     int             `json:"maxOpenPositions"` // nil value: -1
	AutoTP               decimal.Decimal `json:"autoTP"`           // nil value: -1
	AutoSL               decimal.Decimal `json:"autoSL"`           // nil value: -1
	MinTraderSize        decimal.Decimal `json:"minTraderSize"`    // nil value: -1
	TestMode             bool            `json:"testMode"`
	NotifyDest           string          `json:"notifyDest"`
	ModeCloseOnly        bool            `json:"modeCloseOnly"`
	ModeInverse          bool            `json:"modeInverse"`
	ModeNoClose          bool            `json:"modeNoClose"`
	ModeBTCTriggerPrice  decimal.Decimal `json:"modeBTCTriggerPrice"` // nil value: -1
	ModeNoSells          bool            `json:"modeNoSells"`
}

func (bc BotConfig) String() string {
	return fmt.Sprintf(
		"UserID: %s, BotID: %d, Exchange: %s, BinanceID: %s, ExcSpreadDiffPercent: %s, "+
			"Leverage: %s, InitialOpenPercent: %s, MaxAddMultiplier: %s, OpenDelay: %s, AddDelay: %s"+
			"OneCoinMaxPercent: %s, BlacklistCoins: %v, WhitelistCoins: %v, AddPreventionPercent: %s, "+
			"BlockAddsAboveEntry: %t, MaxOpenPositions: %d, AutoTP: %s, AutoSL: %s, MinTraderSize: %sß, "+
			"TestMode: %t, NotifyDest: %s, ModeCloseOnly: %t, ModeInverse: %t, ModeNoClose: %t, "+
			"ModeBTCTriggerPrice: %s, ModeNoSells: %t",
		bc.UserID, bc.BotID, bc.Exchange, bc.BinanceID, bc.ExcSpreadDiffPercent, bc.Leverage,
		bc.InitialOpenPercent, bc.MaxAddMultiplier, bc.OpenDelay, bc.AddDelay, bc.OneCoinMaxPercent,
		bc.BlacklistCoins, bc.WhitelistCoins, bc.AddPreventionPercent, bc.BlockAddsAboveEntry,
		bc.MaxOpenPositions, bc.AutoTP, bc.AutoSL, bc.MinTraderSize, bc.TestMode, bc.NotifyDest,
		bc.ModeCloseOnly, bc.ModeInverse, bc.ModeNoClose, bc.ModeBTCTriggerPrice, bc.ModeNoSells,
	)
}

// getSqlKey given a JSON formatted key, return the key as it matches in the psql tables
// returns (sql key, is the key valid, requires a bot restart)
func GetSqlKey(JsonKey string) (string, bool, bool, reflect.Type) {
	type res struct {
		psql            string
		requiresRestart bool
		expectedType    reflect.Type
	}

	// TODO #1 all of these should be the hardcoded type, not a TypeOf
	fields := map[string]res{
		"botName": {
			psql:            "bot_name",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(""),
		},
		"apiKeyRaw": {
			psql:            "api_key_raw",
			requiresRestart: true,
			expectedType:    reflect.TypeOf(""),
		},
		"apiSecretRaw": {
			psql:            "api_secret_raw",
			requiresRestart: true,
			expectedType:    reflect.TypeOf(""),
		},
		"exchange": {
			psql:            "exchange",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(""),
		},
		"binanceID": {
			psql:            "binance_id",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(""),
		},
		"excSpreadDiffPercent": {
			psql:            "exc_spread_diff_percent",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"leverage": {
			psql:            "leverage",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"initialOpenPercent": {
			psql:            "initial_open_percent",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"maxAddMultiplier": {
			psql:            "max_add_multiplier",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"openDelay": {
			psql:            "open_delay",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"addDelay": {
			psql:            "add_delay",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"oneCoinMaxPercent": {
			psql:            "one_coin_max_percent",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"blacklistCoins": {
			psql:            "blacklist_coins",
			requiresRestart: false,
			expectedType:    reflect.TypeOf([]any{}),
		},
		"whitelistCoins": {
			psql:            "whitelist_coins",
			requiresRestart: false,
			expectedType:    reflect.TypeOf([]any{}),
		},
		"addPreventionPercent": {
			psql:            "add_prevention_percent",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"blockAddsAboveEntry": {
			psql:            "block_adds_above_entry",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(true),
		},
		"maxOpenPositions": {
			psql:            "max_open_positions",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(0),
		},
		"autoTP": {
			psql:            "auto_tp",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"autoSL": {
			psql:            "auto_sl",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"minTraderSize": {
			psql:            "min_trader_size",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"testMode": {
			psql:            "test_mode",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(true),
		},
		"notifyDest": {
			psql:            "notify_dest",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(""),
		},
		"modeCloseOnly": {
			psql:            "mode_close_only",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(true),
		},
		"modeInverse": {
			psql:            "mode_inverse",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(true),
		},
		"modeNoClose": {
			psql:            "mode_no_close",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(true),
		},
		"modeBTCTriggerPrice": {
			psql:            "mode_btc_trigger_price",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(float64(0)),
		},
		"modeNoSells": {
			psql:            "mode_no_sells",
			requiresRestart: false,
			expectedType:    reflect.TypeOf(true),
		},
	}

	if val, ok := fields[JsonKey]; ok {
		return val.psql, ok, val.requiresRestart, val.expectedType
	}

	// return empty and false if the key is not found in our map
	return "", false, false, nil
}

func NewBotConfig(userID string, botID int) BotConfig {
	return BotConfig{
		UserID:               userID,
		BotID:                botID,
		BotName:              "",
		BotStatus:            BotInactive,
		APIKeyRaw:            []byte{},
		APISecretRaw:         []byte{},
		Exchange:             "",
		BinanceID:            "",
		ExcSpreadDiffPercent: decimal.NewFromInt(-1),
		Leverage:             decimal.NewFromInt(1),
		InitialOpenPercent:   decimal.NewFromInt(1),
		MaxAddMultiplier:     decimal.NewFromInt(-1),
		OpenDelay:            decimal.NewFromInt(-1),
		AddDelay:             decimal.NewFromInt(-1),
		OneCoinMaxPercent:    decimal.NewFromInt(-1),
		BlacklistCoins:       []string{},
		WhitelistCoins:       []string{},
		AddPreventionPercent: decimal.NewFromInt(-1),
		BlockAddsAboveEntry:  false,
		MaxOpenPositions:     -1,
		AutoTP:               decimal.NewFromInt(-1),
		AutoSL:               decimal.NewFromInt(-1),
		MinTraderSize:        decimal.NewFromInt(-1),
		TestMode:             true,
		NotifyDest:           "",
		ModeCloseOnly:        false,
		ModeInverse:          false,
		ModeNoClose:          false,
		ModeBTCTriggerPrice:  decimal.NewFromInt(-1),
		ModeNoSells:          false,
	}
}
