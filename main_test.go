package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/golang-jwt/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	"github.com/lightspeed-trading/app/db"
	"github.com/shopspring/decimal"

	"github.com/tealeg/xlsx"
)

var creds map[string]string
var pool *pgxpool.Pool
var TEST_PLAN_NAME = "testPlan"
var bot *Bot
var testingUserID = "TEST-common-userid"
var SAMPLE_WEBHOOK = ""
var BYB_KEY string
var BYB_SECRET string

var APIKEY_ENCRYPTED []byte
var APISECRET_ENCRYPTED []byte

// func TestRunDiscordBotDev(t *testing.T) {
// 	devBotToken := ""

// 	log.Printf("LAUNCHING DISCORD BOT FOR TESTINg")

// 	launchDiscordBot(devBotToken, pool)
// }

func TestMain(m *testing.M) {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// read settings env
	var err error
	envPath := "inputs/settings.env"
	creds, err = godotenv.Read(envPath)
	if err != nil {
		log.Fatalf("failed to read env file, %v", err)
	}

	TESTING = true
	numClients := 50
	LOCAL_PORT = creds["LOCAL_PORT_DEV"]
	STAFF_DISC_WH_URL = creds["STAFF_DISC_WH_URL_DEV"]
	STAFF_RENEWAL_DISC_WH_URL = creds["STAFF_DISC_WH_URL_DEV"]
	common.STAFF_DISC_WH_URL = STAFF_DISC_WH_URL
	SEND_TEST_PLANS = false
	LIGHTSPEED_BOT_PROFILE_PICTURE_URL = creds["LIGHTSPEED_BOT_PROFILE_PICTURE_URL"]
	AUTH0_API_TOKEN = creds["AUTH0_MANAGEMENT_API_TOKEN"]
	WHOP_API_TOKEN = creds["WHOP_API_TOKEN"]
	BYB_KEY = creds["ADMIN_BYBIT_TNET_KEY"]
	BYB_SECRET = creds["ADMIN_BYBIT_TNET_SECRET"]

	keyEncrypted, err1 := db.Encrypt(BYB_KEY, creds["DB_ENCRYPTION_KEY"])
	secretEncrypted, err2 := db.Encrypt(BYB_SECRET, creds["DB_ENCRYPTION_KEY"])
	if err1 != nil || err2 != nil {
		log.Fatalf("failed to encrypt, %v %v", err1, err2)
	}
	APIKEY_ENCRYPTED = keyEncrypted
	APISECRET_ENCRYPTED = secretEncrypted

	// set up pool
	pool, err = db.GetConnPool(creds["DB_NAME"], creds["DB_USER"], creds["DB_PASS"], true)
	if err != nil {
		log.Fatalf("failed to acquire pool, %v", err)
	}
	dbConnPool = pool

	if err := CheckEnvsExist(); err != nil {
		log.Fatalf("TestEnvsExist failed: %v", err)
		os.Exit(1)
	}
	if err := CheckAllSchemas(); err != nil {
		log.Fatalf("TestCheckAllSchemas failed: %v", err)
		os.Exit(1)
	}

	handler = newHandler()
	allClients := make([]*http.Client, numClients)
	for i := 0; i < numClients; i++ {
		allClients[i] = &http.Client{}
	}
	exchanges := map[string]*cls.Exchange{}
	for _, exc := range cls.ValidExchanges {
		exchanges[exc] = cls.NewExchange(exc, allClients, map[string]cls.Symbol{})
	}
	handler.excData = exchanges

	excData, err := getExchangeSymbolData(TESTING)
	if err != nil {
		log.Fatalf("failed to get exchange symbol data, %v", err)
	}
	for excName, symbolData := range excData {
		handler.excData[excName].SymbolData = symbolData
	}

	// set proxies list, in reverse order to the ones in production
	prox, fileName, err := collectProxies()
	if err != nil {
		log.Fatalf("failed to collect proxies, err: %v", err)
	}
	for i := len(prox)/2 - 1; i >= 0; i-- {
		opp := len(prox) - 1 - i
		prox[i], prox[opp] = prox[opp], prox[i]
	}

	log.Printf("successfully collected %d proxies from %s", len(prox), fileName)
	proxies = cls.Proxies{Available: prox, Mu: sync.Mutex{}}

	// setup test bot
	bot, err = getInitedTestBot(ENVPATH)
	if err != nil {
		log.Fatalf("failed to init test bot, %v", err)
	}

	// run tests
	testResult := m.Run()
	os.Exit(testResult)
}

// TODO #28 TestCheckAllSchemas and TestEnvsExist should both run before the rest of the tests - and if they fail, the other tests shouldn't run
func CheckEnvsExist() error {
	checkIsGood := func(key string) bool {
		value, exists := creds[key]
		if !exists || value == "" {
			return false
		}
		return true
	}

	requiredKeys := []string{
		"ADMIN_PHEMEX_KEY", "ADMIN_PHEMEX_SECRET", "ADMIN_BYBIT_TNET_KEY",
		"ADMIN_BYBIT_TNET_SECRET", "DB_USER", "DB_PASS", "DB_NAME", "DB_ENCRYPTION_KEY",
		"STAFF_DISC_WH_URL", "STAFF_DISC_WH_URL_DEV", "DOMAIN_NAME", "OUR_WALLET_ADDRESS",
		"ETH_NETWORK_DIAL_ADDRESS", "TESTING_ETH_NETWORK_DIAL_ADDRESS",
		"ADMIN_GOERLI_ETH_PRIVATE_KEY",
		"ALLOW_SEND_TEST_PLANS", "LIGHTSPEED_BOT_PROFILE_PICTURE_URL",
		"ADMIN_API_KEY", "LIGHTSPEED_HELPER_DISCORD_TOKEN_PROD",
		"LIGHTSPEED_HELPER_DISCORD_TOKEN_DEV", "AUTH0_MANAGEMENT_API_TOKEN",
		"EXPORTED_TRADES_DISCORD_CHANNEL_ID",
		"EXPORT_TRADES_FREQUENCY_IN_DAYS", "STAFF_RENEWAL_DISC_WH_URL",
	}

	for _, key := range requiredKeys {
		if !checkIsGood(key) {
			return fmt.Errorf("key %s doesn't exist", key)
		}
	}

	return nil
}

func CheckAllSchemas() error {
	type namedPool struct {
		pool *pgxpool.Pool
		name string
	}

	pools := make([]namedPool, 2)

	devPool, err1 := db.GetConnPool(creds["DB_NAME"], creds["DB_USER"], creds["DB_PASS"], true)
	prodPool, err2 := db.GetConnPool(creds["DB_NAME"], creds["DB_USER"], creds["DB_PASS"], false)

	if err1 != nil || err2 != nil {
		return fmt.Errorf("%v %v", err1, err2)
	}

	pools[0] = namedPool{
		pool: devPool,
		name: "dev ",
	}
	pools[1] = namedPool{
		pool: prodPool,
		name: "prod",
	}

	// do for both prod and dev databases
	for _, pool := range pools {
		// query the schema of the copytrading table
		rows, err := pool.pool.Query(
			context.Background(),
			`SELECT column_name, data_type, is_nullable
			FROM information_schema.columns
			WHERE table_name = 'copytrading'`,
		)
		if err != nil {
			return fmt.Errorf("Failed to query schema: %v", err)
		}

		// define the expected schema
		expectedSchema := map[string]struct {
			dataType   string
			isNullable string
		}{
			"user_id":                 {"text", "NO"},
			"bot_id":                  {"integer", "NO"},
			"bot_name":                {"text", "YES"},
			"bot_status":              {"text", "YES"},
			"api_key_raw":             {"bytea", "YES"},
			"api_secret_raw":          {"bytea", "YES"},
			"exchange":                {"text", "YES"},
			"binance_id":              {"text", "YES"},
			"exc_spread_diff_percent": {"numeric", "YES"},
			"leverage":                {"numeric", "YES"},
			"initial_open_percent":    {"numeric", "YES"},
			"max_add_multiplier":      {"numeric", "YES"},
			"open_delay":              {"numeric", "YES"},
			"add_delay":               {"numeric", "YES"},
			"one_coin_max_percent":    {"numeric", "YES"},
			"blacklist_coins":         {"ARRAY", "YES"},
			"whitelist_coins":         {"ARRAY", "YES"},
			"add_prevention_percent":  {"numeric", "YES"},
			"block_adds_above_entry":  {"boolean", "YES"},
			"max_open_positions":      {"integer", "YES"},
			"auto_tp":                 {"numeric", "YES"},
			"auto_sl":                 {"numeric", "YES"},
			"min_trader_size":         {"numeric", "YES"},
			"test_mode":               {"boolean", "YES"},
			"notify_dest":             {"text", "YES"},
			"mode_close_only":         {"boolean", "YES"},
			"mode_inverse":            {"boolean", "YES"},
			"mode_no_close":           {"boolean", "YES"},
			"mode_btc_trigger_price":  {"numeric", "YES"},
			"mode_no_sells":           {"boolean", "YES"},
		}

		err = checkSchema(rows, expectedSchema, pool.name)
		if err != nil {
			return err
		}

		// query the schema of the users table
		rows, err = pool.pool.Query(
			context.Background(),
			`SELECT column_name, data_type, is_nullable
			FROM information_schema.columns
			WHERE table_name = 'users'`,
		)
		if err != nil {
			return fmt.Errorf("Failed to query schema: %v", err)
		}

		// define the expected schema for users
		expectedSchema = map[string]struct {
			dataType   string
			isNullable string
		}{
			"user_id":            {"text", "NO"},
			"plan":               {"text", "YES"},
			"renewal_ts":         {"timestamp without time zone", "YES"},
			"alert_webhook":      {"text", "YES"},
			"remaining_cpyt_bal": {"numeric", "YES"},
		}

		// check the actual schema against the expected schema for users
		err = checkSchema(rows, expectedSchema, pool.name)
		if err != nil {
			return err
		}

		// query the schema of the plans table
		rows, err = pool.pool.Query(
			context.Background(),
			`SELECT column_name, data_type, is_nullable
			FROM information_schema.columns
			WHERE table_name = 'plans'`,
		)
		if err != nil {
			return fmt.Errorf("Failed to query schema: %v", err)
		}

		// define the expected schema for plans
		expectedSchema = map[string]struct {
			dataType   string
			isNullable string
		}{
			"name":                   {"text", "NO"},
			"cost_per_month":         {"real", "YES"},
			"comment":                {"text", "YES"},
			"allowed_ct_instances":   {"integer", "YES"},
			"allowed_sign_instances": {"integer", "YES"},
			"max_ct_balance":         {"numeric", "YES"},
			"displayable_name":       {"text", "YES"},
			"is_sold":                {"boolean", "YES"},
			"initial_payment":        {"real", "YES"},
		}

		// check the actual schema against the expected schema for plans
		err = checkSchema(rows, expectedSchema, pool.name)
		if err != nil {
			return err
		}

		// query the schema of the trades table
		rows, err = pool.pool.Query(
			context.Background(),
			`SELECT column_name, data_type, is_nullable
			FROM information_schema.columns
			WHERE table_name = 'trades'`,
		)
		if err != nil {
			return fmt.Errorf("Failed to query schema: %v", err)
		}

		// define the expected schema for trades
		expectedSchema = map[string]struct {
			dataType   string
			isNullable string
		}{
			"id":            {"integer", "NO"},
			"trader_uid":    {"text", "YES"},
			"trade_type":    {"text", "YES"},
			"symbol":        {"text", "YES"},
			"side":          {"text", "YES"},
			"qty":           {"real", "YES"},
			"utc_timestamp": {"timestamp without time zone", "YES"},
			"entry_price":   {"numeric", "YES"},
			"market_price":  {"real", "YES"},
			"pnl":           {"real", "YES"},
		}

		// check the actual schema against the expected schema for trades
		err = checkSchema(rows, expectedSchema, pool.name)
		if err != nil {
			return err
		}
	}

	return nil
}

func TestSetBotActivity(t *testing.T) {
	userID := "TEST-SetBotActivity"
	botID := 1

	truncateTable("copytrading", t)

	// add sample bot config to table
	bc := db.NewBotConfig(userID, botID)
	bc.BotStatus = db.BotInactive
	err := db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}

	setStatusThenCheckSuccess := func(status db.BotStatus) {
		// set it
		err := db.SetBotActivity(userID, botID, status, pool)
		if err != nil {
			t.Fatalf("failed to set bot activity to starting, %v", err)
		}

		// check it wrote successfully
		config, err := db.ReadRecordCPYT(pool, userID, botID)
		if err != nil {
			t.Fatalf("failed to read from copytrading, %v", err)
		}
		if config.BotStatus != status {
			t.Fatalf("when reading status after writing it - expected %s, got %s", status, config.BotStatus)
		}
	}

	// set statuses and check that theyr good after writing them
	setStatusThenCheckSuccess(db.BotStarting)
	setStatusThenCheckSuccess(db.BotActive)
	setStatusThenCheckSuccess(db.BotStopping)
	setStatusThenCheckSuccess(db.BotInactive)
}

func TestOnGetPlans(t *testing.T) {
	prevSettingOfAllowTestPlans := SEND_TEST_PLANS
	SEND_TEST_PLANS = false
	defer func() {
		SEND_TEST_PLANS = prevSettingOfAllowTestPlans
	}()

	truncateTable("plans", t)

	insertTestPlan("plan1", true, 1, t)
	insertTestPlan("plan2", true, 2, t)
	insertTestPlan("plan3", true, 3, t)

	insertTestPlan(TEST_PLAN_NAME, false, 0, t)

	req, resp := createTestRequest("", "", t)

	onGetPlans(resp, req, pool)

	var respJson GetPlansResp
	err := json.NewDecoder(resp.Body).Decode(&respJson)
	if err != nil {
		t.Fatalf("failed to decode json response, %v", err)
	}

	// check response is ok
	if resp.Code != 200 {
		t.Errorf("expected status 200, got: %d", resp.Code)
	}
	if respJson.Err != "" {
		t.Errorf("expected no err, got: %s", respJson.Err)
	}
	if len(respJson.Plans) == 0 {
		t.Error("expected > 0 plans, got 0")
	}
	if t.Failed() {
		t.FailNow()
	}

	// should also print SEND_TEST_PLANS to prevent simple mistakes
	testPlanName := TEST_PLAN_NAME

	_, err = db.ReadPlan(testPlanName, pool)
	if err != nil {
		t.Fatalf("failed to read \"%s\" from plans, %v", testPlanName, err)
	}

	for _, p := range respJson.Plans {
		if p.Name == testPlanName {
			t.Errorf(
				"onGetLicense returned %s when SEND_TEST_PLANS is set to %t",
				p.Name, SEND_TEST_PLANS,
			)
		}
	}

	// now test with SEND_TEST_PLANS == true
	SEND_TEST_PLANS = true

	req, resp = createTestRequest("", "", t)
	onGetPlans(resp, req, pool)

	var respJson2 GetPlansResp
	err = json.NewDecoder(resp.Body).Decode(&respJson2)
	if err != nil {
		t.Fatalf("failed to decode json response, %v", err)
	}

	// check response is ok
	if resp.Code != 200 {
		t.Errorf("expected status 200, got: %d", resp.Code)
	}
	if respJson2.Err != "" {
		t.Errorf("expected no err, got: %s", respJson2.Err)
	}
	if len(respJson2.Plans) == 0 {
		t.Error("expected > 0 plans, got 0")
	}
	if t.Failed() {
		t.FailNow()
	}

	// already know that testPlan is in plans table, so just need to
	// check that it IS in the response, because SEND_TEST_PLANS is true

	testPlanIsPresent := false
	for _, p := range respJson2.Plans {
		if p.Name == testPlanName {
			testPlanIsPresent = true
			break
		}
	}
	if !testPlanIsPresent {
		t.Errorf(
			"onGetLicense didn't send %s with SEND_TEST_PLANS == %t, instead sent plans: %v",
			testPlanName, SEND_TEST_PLANS, respJson2.Plans,
		)
	}
}

func TestSetNumInstances(t *testing.T) {
	truncateTable("copytrading", t)

	pool, err := db.GetConnPool(creds["DB_NAME"], creds["DB_USER"], creds["DB_PASS"], true)
	if err != nil {
		t.Fatalf("failed to acquire pool, %v", err)
	}

	userID := "TestSetNumInstances-UserID"

	t.Run("Increase instances", func(t *testing.T) {
		status, err := SetNumInstances(userID, 5, pool)
		if err != nil || status != 200 {
			t.Fatalf("Expected success; got status: %d, error: %v", status, err)
		}

		instances, _ := db.ReadAllRecordsCPYT(pool, userID)
		if len(instances) != 5 {
			t.Errorf("Expected 5 instances; got %d", len(instances))
		}
	})
	if t.Failed() {
		t.FailNow()
	}

	t.Run("Decrease instances", func(t *testing.T) {
		status, err := SetNumInstances(userID, 3, pool)
		if err != nil || status != 200 {
			t.Fatalf("Expected success; got status: %d, error: %v", status, err)
		}

		instances, _ := db.ReadAllRecordsCPYT(pool, userID)
		if len(instances) != 3 {
			t.Errorf("Expected 3 instances; got %d", len(instances))
		}
	})
	if t.Failed() {
		t.FailNow()
	}

	t.Run("No change in instances", func(t *testing.T) {
		status, err := SetNumInstances(userID, 3, pool)
		if err != nil || status != 200 {
			t.Fatalf("Expected err == nil; got status: %d, error: %v", status, err)
		}
	})
}

func TestOnUpdateSettings(t *testing.T) {
	truncateTable("copytrading", t)

	userID := "userid1"

	// insert pre-update initial value in
	key, err := db.Encrypt("OWRTFHDOGRWBSXKURV", creds["DB_ENCRYPTION_KEY"])
	if err != nil {
		t.Fatalf("failed to encrypt key, %v", err)
	}
	secret, err := db.Encrypt("QQGXNBTMRPYRRUPCQIMQYNBXFVQPSBARTZRH", creds["DB_ENCRYPTION_KEY"])
	if err != nil {
		t.Fatalf("failed to encrypt secret, %v", err)
	}

	bc := db.BotConfig{
		UserID:               userID,
		BotID:                1,
		BotName:              "myCoolBot",
		BotStatus:            db.BotActive,
		APIKeyRaw:            key,
		APISecretRaw:         secret,
		Exchange:             "bybit",
		BinanceID:            "C20E7A8966C0014A4AF5774DD709DC42",
		ExcSpreadDiffPercent: decimal.NewFromFloat(0.5),
		Leverage:             decimal.NewFromInt(10),
		InitialOpenPercent:   decimal.NewFromInt(3),
		MaxAddMultiplier:     decimal.NewFromInt(2),
		OpenDelay:            decimal.NewFromInt(30),
		AddDelay:             decimal.NewFromInt(15),
		OneCoinMaxPercent:    decimal.NewFromInt(50),
		BlacklistCoins:       []string{},
		WhitelistCoins:       strings.Split("BTCUSDT ETHUSDT ADAUSDT", " "),
		AddPreventionPercent: decimal.NewFromInt(5),
		BlockAddsAboveEntry:  false,
		MaxOpenPositions:     5,
		AutoTP:               decimal.NewFromInt(30),
		AutoSL:               decimal.NewFromInt(20),
		MinTraderSize:        decimal.NewFromInt(500),
		TestMode:             true,
		NotifyDest:           "https://discord.com/api/webhooks/1109478837546389524/Q-Di8Zt4wUsXIzMEiUVs-UI1t-w-2pxhID3989-vGTuTiH4jmrmi_asdasdD1EFODVnT",
		ModeCloseOnly:        false,
		ModeInverse:          false,
		ModeNoClose:          false,
		ModeBTCTriggerPrice:  decimal.NewFromInt(-1),
		ModeNoSells:          false,
	}

	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to insert pre update value, %v", err)
	}

	// create request
	bodyStruct := UpdateSettingsRequest{
		BotID:        1,
		OldTraderUID: "C20E7A8966C0014A4AF5774DD709DC42",
		Settings: map[string]interface{}{
			"leverage":           15.0,
			"botName":            "foo",
			"apiSecretRaw":       "anotherSecretKey",
			"blacklistCoins":     []string{"BTCUSDT", "ethusdt"},
			"whitelistCoins":     []string{"BTCUSDT", "ethusdt"},
			"maxOpenPositions":   1,
			"modeNoSells":        "I should be a bool!",
			"keyThatDoesntExist": "I don't exist...",
			"notifyDest":         "bad webhook, needs to start with https://discord... etc",
		},
	}
	dummyLMC := cls.NewLMC()

	req, resp := createTestRequest(bodyStruct, userID, t)
	onUpdateSettings(resp, req, pool, creds["DB_ENCRYPTION_KEY"], true, &dummyLMC)

	if resp.Code != 400 {
		t.Errorf("expected 400 status, got %d", resp.Code)
	}

	var updateSettingsResp UpdateSettingsResponse
	err = json.NewDecoder(resp.Body).Decode(&updateSettingsResp)
	if err != nil {
		t.Fatalf("failed to decode json response, %v", err)
	}

	// check that the right things are in the failed response
	failed := updateSettingsResp.UpdateFailed
	sort.Strings(failed)
	if strings.Join(failed, "") != "blacklistCoinskeyThatDoesntExistmodeNoSellsnotifyDest" {
		t.Fatalf("got bad failed, %v", failed)
	}

	// read db check it looks good (ie no changes)
	config, err := db.ReadRecordCPYT(pool, userID, 1)
	if err != nil {
		t.Fatalf("failed to get config from db, %v", err)
	}

	if err := db.CheckBotConfigEquality(bc, config, creds["DB_ENCRYPTION_KEY"]); err != nil {
		t.Fatal(err)
	}

	// now make request with just correct keys
	newSecret := "anotherSecretKey"
	bodyStruct = UpdateSettingsRequest{
		BotID:        1,
		OldTraderUID: "C20E7A8966C0014A4AF5774DD709DC42",
		Settings: map[string]interface{}{
			"leverage":         15.0,
			"botName":          "foo",
			"apiSecretRaw":     newSecret,
			"whitelistCoins":   []string{"BTCUSDT", "ethusdt"},
			"maxOpenPositions": 1,
			"notifyDest":       SAMPLE_WEBHOOK,
		},
	}

	req, resp = createTestRequest(bodyStruct, userID, t)
	onUpdateSettings(resp, req, pool, creds["DB_ENCRYPTION_KEY"], true, &cls.LaunchingMonitorsCache{})

	if resp.Code != 200 {
		t.Errorf("expected 200 status, got %d", resp.Code)
	}

	err = json.NewDecoder(resp.Body).Decode(&updateSettingsResp)
	if err != nil {
		t.Fatalf("failed to decode json response, %v", err)
	}

	expectedSecret, err := db.Encrypt(newSecret, creds["DB_ENCRYPTION_KEY"])
	if err != nil {
		t.Fatalf("failed to encrypt, %v", err)
	}

	expectedConfig := db.BotConfig{
		UserID:               userID,
		BotID:                1,
		BotName:              "foo",
		BotStatus:            db.BotActive,
		APIKeyRaw:            key,
		APISecretRaw:         expectedSecret,
		Exchange:             "bybit",
		BinanceID:            "C20E7A8966C0014A4AF5774DD709DC42",
		ExcSpreadDiffPercent: decimal.NewFromFloat(0.5),
		Leverage:             decimal.NewFromFloat(15.0),
		InitialOpenPercent:   decimal.NewFromInt(3),
		MaxAddMultiplier:     decimal.NewFromInt(2),
		OpenDelay:            decimal.NewFromInt(30),
		AddDelay:             decimal.NewFromInt(15),
		OneCoinMaxPercent:    decimal.NewFromInt(50),
		BlacklistCoins:       []string{},
		WhitelistCoins:       []string{"BTCUSDT", "ETHUSDT"},
		AddPreventionPercent: decimal.NewFromInt(5),
		BlockAddsAboveEntry:  false,
		MaxOpenPositions:     1,
		AutoTP:               decimal.NewFromInt(30),
		AutoSL:               decimal.NewFromInt(20),
		MinTraderSize:        decimal.NewFromInt(500),
		TestMode:             true,
		NotifyDest:           "https://discord.com/api/webhooks/1109478837546389524/Q-Di8Zt4wUsXIzMEiUVs-UI1t-w-2pxhID3989-vGTuTiH4jmrmi_whyMlID1EFODVnT",
		ModeCloseOnly:        false,
		ModeInverse:          false,
		ModeNoClose:          false,
		ModeBTCTriggerPrice:  decimal.NewFromInt(-1),
		ModeNoSells:          false,
	}

	// read db check it looks good (ie no changes)
	config, err = db.ReadRecordCPYT(pool, userID, 1)
	if err != nil {
		t.Fatalf("failed to get config from db, %v", err)
	}

	if err = db.CheckBotConfigEquality(config, expectedConfig, creds["DB_ENCRYPTION_KEY"]); err != nil {
		t.Fatal(err)
	}
}

func TestOnGetLicense_Success(t *testing.T) {
	userID := "TEST-OnGetLicense-Success"
	renewalTime := time.Now().UTC().AddDate(0, 0, 1).Round(time.Second)

	// truncate table
	truncateTable("users", t)

	// write to users table
	sampleUser := db.User{
		UserID:           userID,
		Plan:             "copytradeTier2",
		RenewalTS:        renewalTime,
		AlertWebhook:     "",
		RemainingCpytBal: decimal.NewFromInt(3510),
	}
	err := db.WriteRecordUsers(sampleUser, pool)
	if err != nil {
		t.Fatalf("failed to write to users table, %v", err)
	}

	// make request
	req, resp := createTestRequest("", userID, t)
	onGetLicense(resp, req, pool)

	// check is as expected
	if resp.Code != 200 {
		t.Errorf("expected 200 status, got %d", resp.Code)
	}

	var respJson GetLicenseResponse
	err = json.NewDecoder(resp.Body).Decode(&respJson)
	if err != nil {
		t.Fatalf("failed to decode json response, %v", err)
	}

	gotUser := db.User{
		UserID:           "",
		Plan:             respJson.Plan,
		RenewalTS:        time.Unix(int64(respJson.RenewalTS), 0).UTC(),
		AlertWebhook:     respJson.AlertWebhook,
		RemainingCpytBal: respJson.RemainingCpytBal,
	}
	sampleUser.UserID = ""
	if !reflect.DeepEqual(gotUser, sampleUser) {
		t.Fatalf(
			`got and expected not equal,
			got:      %v
			expected: %v`, gotUser, sampleUser,
		)
	}
}

func TestOnGetLicense_Fail(t *testing.T) {
	userID := "TestOnGetLicense_Fail"

	truncateTable("users", t)

	// don't instert anything into users table, needs to return bad plan

	req, resp := createTestRequest(struct{}{}, userID, t)
	onGetLicense(resp, req, pool)

	if resp.Code != 200 {
		t.Fatalf("expected 200 status, got: %d", resp.Code)
	}

	var respBody GetLicenseResponse
	err := json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body json, %v", err)
	}

	if respBody.Plan != "" {
		t.Fatalf("expected plan: \"\", got: %s", respBody.Plan)
	}
	if respBody.Err != "" {
		t.Fatalf("expected err: \"\", got: %s", respBody.Err)
	}
}

func TestOnListBots(t *testing.T) {
	userID := "TEST-OnListBots"
	numInstances := 3

	truncateTable("copytrading", t)

	// write to table
	for i := 1; i <= 3; i++ {
		bc := db.NewBotConfig(userID, i)
		err := db.WriteRecordCPYT(pool, bc)
		if err != nil {
			t.Fatalf("failed to write to copytrading, %v", err)
		}
	}

	// read from table
	bots, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		t.Fatalf("failed to read all bots, %v", err)
	}

	if len(bots) != numInstances {
		t.Fatalf("expected %d instances, got %d", numInstances, len(bots))
	}
}

func TestInitialStorageAndDeletion(t *testing.T) {
	userID := "TEST-InitialStorageAndDeletion-Success"
	planName := "copytradeTier1"

	truncateTable("copytrading", t)
	truncateTable("users", t)
	truncateTable("plans", t)

	insertTestPlan("copytradeTier1", true, 4, t)

	// store data
	err := storeDataOnInitialPayment(userID, planName, pool)
	if err != nil {
		t.Fatalf("failed to store data after a successful payment, %v", err)
	}

	// read plan
	plan, err := db.ReadPlan(planName, pool)
	if err != nil {
		t.Fatalf("failed to read from plans table, %v", err)
	}

	// check if it was stored correctly
	expectedConfig := db.NewBotConfig(userID, -1)
	for iter := 1; iter <= plan.AllowedCTInstances; iter++ {
		config, err := db.ReadRecordCPYT(pool, userID, iter)
		if err != nil {
			t.Fatalf("failed to read record from copytrading, %v", err)
		}

		expectedConfig.BotID = iter
		if !reflect.DeepEqual(config, expectedConfig) {
			t.Fatalf("expected configs not equal, \ngot:%v, \nexpected:%v", config, expectedConfig)
		}
	}

	user, err := db.ReadRecordUsers(userID, pool)
	if err != nil {
		t.Fatalf("failed to read from users table, %v", err)
	}
	expectedBal := plan.MaxCTBalance
	gotBal := user.RemainingCpytBal
	if !user.RemainingCpytBal.Equal(expectedBal) {
		t.Fatalf("expected user to have bal %s, got: %s", expectedBal, gotBal)
	}

	// check that multi read works
	configs, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		t.Fatalf("failed to ReadAllRecordsCPYT, %v", err)
	}
	if len(configs) != plan.AllowedCTInstances {
		t.Fatalf("expected %d configs, got %d", plan.AllowedCTInstances, len(configs))
	}

	// delete from tables
	err = db.WipeUser(pool, userID, true, true)
	if err != nil {
		t.Fatalf("failed to wipe user, %v", err)
	}

	rows, err := pool.Query(
		context.Background(),
		"SELECT * FROM copytrading WHERE user_id = $1",
		userID,
	)
	if err != nil {
		t.Fatalf("failed to execute query: %v", err)
	}

	// if there is a next row, that means the deletion did not occur as expected
	if rows.Next() {
		t.Fatalf("user was not deleted, rows are returned")
	}
	rows.Close()

	// check err
	if err := rows.Err(); err != nil {
		t.Fatalf("an error occurred during rows iteration: %v", err)
	}

	// do the same for users table
	rows, err = pool.Query(
		context.Background(),
		"SELECT * FROM users WHERE user_id = $1",
		userID,
	)
	if err != nil {
		t.Fatalf("failed to execute query: %v", err)
	}

	if rows.Next() {
		t.Fatalf("user was not deleted, rows are returned")
	}
	rows.Close()
}

func TestGetETHPrice(t *testing.T) {
	_, err := getEthPrice()

	if err != nil {
		t.Fatalf("failed to get eth price, %v", err)
	}
}

func TestOnAdminSetUserPlan_Success(t *testing.T) {
	truncateTable("users", t)
	truncateTable("copytrading", t)
	truncateTable("plans", t)

	userID := "TestOnAdminSetUserPlan-TargetUserID"
	copytradeTier1PlanName := "copytradeTier1"

	insertTestPlan(copytradeTier1PlanName, true, 5, t)

	ten := 10
	reqBody := SetUserPlanReq{
		TargetUserID:      userID,
		PlanName:          copytradeTier1PlanName,
		OverwriteExisting: false,
		DaysUntilRenewal:  &ten,
	}

	req, resp := createTestRequest(reqBody, "", t)
	req.Header.Add("LS-API-Key", creds["ADMIN_API_KEY"])

	onAdminAddPlanToUser(resp, req, pool, creds["ADMIN_API_KEY"])

	// check resp is good
	if resp.Code != 200 {
		t.Errorf("expected resp status: 200, got: %d", resp.Code)
	}

	var respBody SetUserPlanResp
	err := json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	if respBody.Err != "" {
		t.Fatalf("expected no err, got: %s", respBody.Err)
	}

	// check users state is good
	user, err := db.ReadRecordUsers(userID, pool)
	if err != nil {
		t.Fatalf("failed to read from users, %v", err)
	}
	if user.Plan != copytradeTier1PlanName {
		t.Errorf("expected user.Plan: %s, got: %s", copytradeTier1PlanName, user.Plan)
	}

	expectedRenewalTS := time.Now().Add(10 * 24 * time.Hour).In(time.UTC)
	tolerance := 1 * time.Minute // adjust the tolerance as needed

	actualDuration := user.RenewalTS.Sub(expectedRenewalTS)
	if AbsDuration(actualDuration) > tolerance {
		t.Errorf(
			"expected user.RenewalTS to be within %v of %s; got %s, difference: %v",
			tolerance, expectedRenewalTS, user.RenewalTS, actualDuration,
		)
	}

	// check copytrading
	bots, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		t.Fatalf("failed to read all rows from copytrading, %v", err)
	}
	plan, err := db.ReadPlan(copytradeTier1PlanName, pool)
	if err != nil {
		t.Fatalf("failed to read %s from plans, %v", copytradeTier1PlanName, err)
	}

	expectedBots := plan.AllowedCTInstances
	gotBots := len(bots)

	if expectedBots != gotBots {
		t.Errorf("expected %d bots, got: %d", expectedBots, gotBots)
	}
}

func TestOnAdminSetUserPlan_WithOverwrite_Success(t *testing.T) {
	truncateTable("users", t)
	truncateTable("copytrading", t)
	truncateTable("plans", t)

	userID := "TestOnAdminSetUserPlan-TargetUserID"
	copytradeTier1PlanName := "copytradeTier1"
	copytradeTier2PlanName := "copytradeTier2"

	// write to db first
	copytradeTier2 := db.Plan{
		Name:                 copytradeTier2PlanName,
		CostPerMonth:         50,
		Comment:              "testCopytradeTier2",
		AllowedCTInstances:   3,
		AllowedSignInstances: 2,
		MaxCTBalance:         decimal.NewFromInt(5000),
		DisplayableName:      "copytrade tier 2",
		IsSold:               false,
		InitialPayment:       200,
	}
	err := db.WriteRecordPlans(copytradeTier2, pool)
	if err != nil {
		t.Fatalf("failed to write to plans, %v", err)
	}

	copytradeTier1 := db.Plan{
		Name:                 copytradeTier1PlanName,
		CostPerMonth:         25,
		Comment:              "testCopytradeTier1",
		AllowedCTInstances:   3,
		AllowedSignInstances: 0,
		MaxCTBalance:         decimal.NewFromInt(2500),
		DisplayableName:      "copytrade tier 1",
		IsSold:               false,
		InitialPayment:       200,
	}
	err = db.WriteRecordPlans(copytradeTier1, pool)
	if err != nil {
		t.Fatalf("failed to write to plans, %v", err)
	}

	writeUser := db.User{
		UserID:           userID,
		Plan:             copytradeTier2PlanName,
		RenewalTS:        time.Time{},
		AlertWebhook:     "",
		RemainingCpytBal: decimal.Zero,
	}
	err = db.WriteRecordUsers(writeUser, pool)
	if err != nil {
		t.Fatalf("failed to write to users, %v", err)
	}

	numInstancesToCreate := 8
	for iter := 1; iter <= numInstancesToCreate; iter++ {
		err := db.WriteRecordCPYT(
			pool,
			db.NewBotConfig(userID, iter),
		)
		if err != nil {
			t.Fatalf("failed to write to copytrading table at iter %d, %v", iter, err)
		}
	}

	ten := 10
	reqBody := SetUserPlanReq{
		TargetUserID:      userID,
		PlanName:          copytradeTier1PlanName,
		OverwriteExisting: true,
		DaysUntilRenewal:  &ten,
	}

	req, resp := createTestRequest(reqBody, "", t)
	req.Header.Add("LS-API-Key", creds["ADMIN_API_KEY"])

	onAdminAddPlanToUser(resp, req, pool, creds["ADMIN_API_KEY"])

	// check resp is good
	if resp.Code != 200 {
		t.Errorf("expected resp status: 200, got: %d", resp.Code)
	}

	var respBody SetUserPlanResp
	err = json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	if respBody.Err != "" {
		t.Fatalf("expected no err, got: %s", respBody.Err)
	}

	// check users state is good
	user, err := db.ReadRecordUsers(userID, pool)
	if err != nil {
		t.Fatalf("failed to read from users, %v", err)
	}
	if user.Plan != copytradeTier1PlanName {
		t.Errorf("expected user.Plan: %s, got: %s", copytradeTier1PlanName, user.Plan)
	}

	expectedRenewalTS := time.Now().Add(10 * 24 * time.Hour).In(time.UTC)
	tolerance := 1 * time.Minute // adjust the tolerance as needed

	actualDuration := user.RenewalTS.Sub(expectedRenewalTS)
	if AbsDuration(actualDuration) > tolerance {
		t.Errorf(
			"expected user.RenewalTS to be within %v of %s; got %s, difference: %v",
			tolerance, expectedRenewalTS, user.RenewalTS, actualDuration,
		)
	}

	// check copytrading
	bots, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		t.Fatalf("failed to read all rows from copytrading, %v", err)
	}
	plan, err := db.ReadPlan(copytradeTier1PlanName, pool)
	if err != nil {
		t.Fatalf("failed to read %s from plans, %v", copytradeTier1PlanName, err)
	}

	expectedBots := plan.AllowedCTInstances
	gotBots := len(bots)

	if expectedBots != gotBots {
		t.Errorf("expected %d bots, got: %d", expectedBots, gotBots)
	}
}

func TestDialMainnetEthClient(t *testing.T) {
	_, err := ethclient.Dial(creds["ETH_NETWORK_DIAL_ADDRESS"])
	if err != nil {
		log.Printf("failed to connect to the Ethereum client, %v", err)
	}
}

func TestCreateNewPlan_Success(t *testing.T) {
	truncateTable("plans", t) // assuming you have this function to clean the table

	adminKey := "someAdminKey"
	planName := "TestCreateNewPlan_Success"

	// create request
	reqBody := CreateNewPlanReq{
		Plan: db.Plan{
			Name:                 planName,
			CostPerMonth:         10.0,
			Comment:              "goodComment",
			AllowedCTInstances:   2,
			AllowedSignInstances: 3,
			MaxCTBalance:         decimal.NewFromInt(500),
			DisplayableName:      "My_New_Plan!",
			IsSold:               true,
			InitialPayment:       5.0,
		},
		OverwriteExisting: false,
	}

	req, resp := createTestRequest(reqBody, "", t)
	req.Header.Add("LS-API-Key", adminKey)

	onAdminCreateNewPlan(resp, req, pool, adminKey)

	// check resp
	if resp.Code != 200 {
		t.Fatalf("expected resp status: 200, got: %d", resp.Code)
	}

	var respBody CreateNewPlanResp
	err := json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	if respBody.Err != "" {
		t.Fatalf("expected no error, got: %s", respBody.Err)
	}

	// check db
	plan, err := db.ReadPlan(planName, pool)
	if err != nil {
		t.Fatalf("failed to read plan from database, %v", err)
	}
	if plan.Name != planName {
		t.Errorf("expected plan name to be %s, got %s", planName, plan.Name)
	}
}

func TestCreateNewPlan_WithOverwrite_Success(t *testing.T) {
	truncateTable("plans", t)

	adminKey := "someAdminKey"
	planName := TEST_PLAN_NAME

	// insert a plan first, to simulate an existing plan
	_, err := pool.Exec(
		context.Background(),
		"INSERT INTO plans (name, cost_per_month, comment, allowed_ct_instances, allowed_sign_instances, max_ct_balance, displayable_name, is_sold, initial_payment) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		planName, 15.0, "Old comment",
		1, 2, 50, "Old Displayable TestPlan", true, 8.0,
	)
	if err != nil {
		t.Fatalf("failed to insert initial plan, %v", err)
	}

	// create request
	reqBody := CreateNewPlanReq{
		Plan: db.Plan{
			Name:                 planName,
			CostPerMonth:         10.0,
			Comment:              "New comment",
			AllowedCTInstances:   2,
			AllowedSignInstances: 3,
			MaxCTBalance:         decimal.NewFromInt(100),
			DisplayableName:      "New Displayable TestPlan",
			IsSold:               true,
			InitialPayment:       5.0,
		},
		OverwriteExisting: true,
	}

	req, resp := createTestRequest(reqBody, "", t)
	req.Header.Add("LS-API-Key", adminKey)

	onAdminCreateNewPlan(resp, req, pool, adminKey)

	// check resp
	if resp.Code != 200 {
		t.Fatalf("expected resp status: 200, got: %d", resp.Code)
	}

	var respBody CreateNewPlanResp
	err = json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	if respBody.Err != "" {
		t.Fatalf("expected no error, got: %s", respBody.Err)
	}

	// check db
	plan, err := db.ReadPlan(planName, pool)
	if err != nil {
		t.Fatalf("failed to read plan from database, %v", err)
	}
	if plan.Name != planName || plan.Comment != "New comment" {
		t.Errorf("expected plan name to be %s and comment to be 'New comment', got %s and %s", planName, plan.Name, plan.Comment)
	}
}

func TestOnDeletePlan_Success(t *testing.T) {
	truncateTable("plans", t)

	adminKey := "someAdminKey"
	planName := TEST_PLAN_NAME

	// Create and insert a plan first to simulate an existing plan
	_, err := pool.Exec(
		context.Background(),
		"INSERT INTO plans (name, cost_per_month, comment, allowed_ct_instances, allowed_sign_instances, max_ct_balance, displayable_name, is_sold, initial_payment) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		planName, 15.0, "Comment", 1, 2, 50, "Displayable TestPlan", true, 8.0,
	)
	if err != nil {
		t.Fatalf("failed to insert initial plan, %v", err)
	}

	reqBody := DeletePlanReq{
		PlanName: planName,
	}

	req, resp := createTestRequest(reqBody, "", t)
	req.Header.Add("LS-API-Key", adminKey)

	// Call function
	onAdminDeletePlan(resp, req, pool, adminKey)

	// Validate response
	if resp.Code != 200 {
		t.Fatalf("expected resp status: 200, got: %d", resp.Code)
	}

	var respBody DeletePlanResp
	err = json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	if respBody.Err != "" {
		t.Fatalf("expected no error, got: %s", respBody.Err)
	}

	// Validate that the plan no longer exists in the database
	_, err = db.ReadPlan(planName, pool)
	if err == nil || !strings.HasSuffix(err.Error(), "no rows in result set") {
		t.Errorf("expected plan to be deleted, got error: %v", err)
	}
}

func TestOnDeletePlan_PlanDoesNotExist(t *testing.T) {
	truncateTable("plans", t)

	adminKey := "someAdminKey"
	nonExistentPlanName := "NonExistentPlan"

	reqBody := DeletePlanReq{
		PlanName: nonExistentPlanName,
	}

	req, resp := createTestRequest(reqBody, "", t)
	req.Header.Add("LS-API-Key", adminKey)

	// Call function
	onAdminDeletePlan(resp, req, pool, adminKey)

	// Validate response
	if resp.Code != 200 {
		t.Fatalf("expected resp status: 200, got: %d", resp.Code)
	}

	var respBody DeletePlanResp
	err := json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	if respBody.Err != "" {
		t.Fatalf("expected no error, got: %s", respBody.Err)
	}

	// Validate that the plan does not exist in the database
	_, err = db.ReadPlan(nonExistentPlanName, pool)
	if err == nil || !strings.HasSuffix(err.Error(), "no rows in result set") {
		t.Errorf("expected plan not to exist, got error: %v", err)
	}
}

func TestOnAdminSetUserPlan_BadAPIKey(t *testing.T) {
	truncateTable("users", t)
	truncateTable("copytrading", t)

	userID := "TestOnAdminSetUserPlan-TargetUserID"
	copytradeTier1PlanName := "copytradeTier1"

	ten := 10
	reqBody := SetUserPlanReq{
		TargetUserID:      userID,
		PlanName:          copytradeTier1PlanName,
		OverwriteExisting: false,
		DaysUntilRenewal:  &ten,
	}

	req, resp := createTestRequest(reqBody, "", t)
	req.Header.Add("LS-API-Key", "badApiKey")

	onAdminAddPlanToUser(resp, req, pool, creds["ADMIN_API_KEY"])

	expectedStatus := 401
	if resp.Code != expectedStatus {
		t.Fatalf("expected status: %d, got: %d", expectedStatus, resp.Code)
	}
}

func TestOnAdminSetUserPlan_NoAPIKey(t *testing.T) {
	truncateTable("users", t)
	truncateTable("copytrading", t)

	userID := "TestOnAdminSetUserPlan-TargetUserID"
	copytradeTier1PlanName := "copytradeTier1"

	ten := 10
	reqBody := SetUserPlanReq{
		TargetUserID:      userID,
		PlanName:          copytradeTier1PlanName,
		OverwriteExisting: false,
		DaysUntilRenewal:  &ten,
	}

	req, resp := createTestRequest(reqBody, "", t)

	onAdminAddPlanToUser(resp, req, pool, creds["ADMIN_API_KEY"])

	expectedStatus := 401
	if resp.Code != expectedStatus {
		t.Fatalf("expected status: %d, got: %d", expectedStatus, resp.Code)
	}
}

func TestOnStartBot_FailNoUserExists(t *testing.T) {
	userID := "TEST-OnStartBot-FailNoUserExists"
	botID := 3

	// create and run test request
	reqParam := StartBotReq{
		BotID: botID,
	}
	req, resp := createTestRequest(reqParam, userID, t)
	onStartBot(resp, req, pool, &cls.LaunchingMonitorsCache{},
		creds["DB_ENCRYPTION_KEY"])

	// decode body
	var respData StartBotResp
	err := json.NewDecoder(resp.Body).Decode(&respData)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	// parse
	if resp.Code != 400 {
		t.Errorf("expected 400 status, got %d", resp.Code)
	}
	expectedRespErr := fmt.Sprintf("Bot %d does not exist", botID)
	if respData.Err != expectedRespErr {
		t.Errorf("expected %s, got %s", expectedRespErr, respData.Err)
	}
}

func TestOnSetUserAlertWebhook_Success(t *testing.T) {
	truncateTable("users", t)

	userID := "TestOnSetUserAlertWebhook_Success"

	// populate with sample data
	sampleUser := db.User{
		UserID:           userID,
		Plan:             TEST_PLAN_NAME,
		RenewalTS:        time.Time{},
		AlertWebhook:     "",
		RemainingCpytBal: decimal.NewFromInt(500),
	}

	err := db.WriteRecordUsers(sampleUser, pool)
	if err != nil {
		t.Fatalf("failed to write to users, %v", err)
	}

	// make dummy request
	reqBody := SetUserAlertWebhookRequest{
		Webhook: SAMPLE_WEBHOOK,
	}
	req, resp := createTestRequest(reqBody, userID, t)
	onSetUserAlertWebhook(resp, req, pool)

	// check result
	var respBody SetUserAlertWebhookResponse
	err = json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	expectedStatus := 200
	if resp.Code != expectedStatus {
		t.Errorf("expected status: %d, got: %d", expectedStatus, resp.Code)
	}
	expectedBodyErr := ""
	if respBody.Err != expectedBodyErr {
		t.Errorf("expected body.Err: %s, got: %s", expectedBodyErr, respBody.Err)
	}
}

func TestOnSetUserAlertWebhook_FailNoUser(t *testing.T) {
	truncateTable("users", t)

	userID := "TestOnSetUserAlertWebhook_FailNoUser"

	// populate with sample data
	sampleUser := db.User{
		UserID:           userID,
		Plan:             TEST_PLAN_NAME,
		RenewalTS:        time.Time{},
		AlertWebhook:     "",
		RemainingCpytBal: decimal.NewFromInt(500),
	}

	err := db.WriteRecordUsers(sampleUser, pool)
	if err != nil {
		t.Fatalf("failed to write to users, %v", err)
	}

	// make dummy request
	reqBody := SetUserAlertWebhookRequest{
		Webhook: SAMPLE_WEBHOOK,
	}
	fakeUserID := userID + "+1"
	req, resp := createTestRequest(reqBody, fakeUserID, t)
	onSetUserAlertWebhook(resp, req, pool)

	// check result
	var respBody SetUserAlertWebhookResponse
	err = json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		t.Fatalf("failed to decode resp body, %v", err)
	}

	expectedStatus := 400
	if resp.Code != expectedStatus {
		t.Errorf("expected status: %d, got: %d", expectedStatus, resp.Code)
	}
	expectedBodyErr := "can't update, no existing user found"
	if respBody.Err != expectedBodyErr {
		t.Errorf("expected body.Err: %s, got: %s", expectedBodyErr, respBody.Err)
	}
}

func TestGetUsersForNotification_Success(t *testing.T) {
	truncateTable("users", t)

	// need users fulfilling the following requirements:
	/*
		5 days away -> shouldnt exist in the result
		-20 mins away -> should exist in result, and should be wiped after
		2 days 3 hours -> shoudln't exist
		1 day 30 mins -> should exist
		2 days 12 mins -> should exist
		3 days 50 mins -> should exist
	*/

	timeDay := 24 * time.Hour
	sampleDurations := []time.Duration{
		5 * timeDay,
		-20 * time.Minute,
		2*timeDay + 3*time.Hour,
		1*timeDay + 30*time.Minute,
		2*timeDay + 12*time.Minute,
		3*timeDay + 50*time.Minute,
	}

	timeNow := time.Now().In(time.UTC)

	for ind, dur := range sampleDurations {
		sampleUser := db.User{
			UserID:           "TestGetUsersForNotification_Success" + fmt.Sprint(ind),
			Plan:             TEST_PLAN_NAME,
			RenewalTS:        timeNow.Add(dur),
			AlertWebhook:     SAMPLE_WEBHOOK,
			RemainingCpytBal: decimal.NewFromInt(500),
		}

		err := db.WriteRecordUsers(sampleUser, pool)
		if err != nil {
			t.Fatalf("failed to write user, %v", err)
		}
	}

	notiUsers, err := getUsersForNotification(pool)
	if err != nil {
		t.Fatalf("failed to get users for notifications, %v", err)
	}

	if len(notiUsers) != 4 {
		t.Errorf("got %d noti users, expected 4", len(notiUsers))
		t.Errorf("noti users: %v", notiUsers)
	}
	for _, nu := range notiUsers {
		if nu.UserID == "TestGetUsersForNotification_Success1" && nu.DaysAway == -1 {
			continue
		}
		if nu.UserID == "TestGetUsersForNotification_Success3" && nu.DaysAway == 1 {
			continue
		}
		if nu.UserID == "TestGetUsersForNotification_Success4" && nu.DaysAway == 2 {
			continue
		}
		if nu.UserID == "TestGetUsersForNotification_Success5" && nu.DaysAway == 3 {
			continue
		}

		t.Fatalf("got unexpected notification user, %v", nu)
	}
	if t.Failed() {
		t.FailNow()
	}

	// now run the noti user stuff
	var wg sync.WaitGroup
	for _, user := range notiUsers {
		wg.Add(1)
		go func(user cls.NotificationUser) {
			defer wg.Done()
			handleIndividualApproachingRenewal(user, pool)
		}(user)
	}
	wg.Wait()

	// now check if the -1 days one has been deleted
	user, err := db.ReadRecordUsers("TestGetUsersForNotification_Success1", pool)
	if err != nil {
		if !errors.Is(err, db.ErrNoUser) {
			t.Fatalf("expected err: %v, got: %v", db.ErrNoUser, err)
		}
	} else {
		t.Fatalf("expected no user and err, got user: %v", user)
	}

	// make sure all the rest come up as still there
	userIDList := []string{
		"TestGetUsersForNotification_Success3",
		"TestGetUsersForNotification_Success4",
		"TestGetUsersForNotification_Success5",
	}

	for _, userID := range userIDList {
		_, err := db.ReadRecordUsers(userID, pool)
		if err != nil {
			t.Fatalf("expected nil err, got: %v", err)
		}
	}
}

func TestSendRenewalNotification_Success(t *testing.T) {
	notificationUser := cls.NotificationUser{
		UserID:          "TestSendRenewalNotification_Success",
		Plan:            TEST_PLAN_NAME,
		RenewalTS:       time.Now().Add(24 * time.Hour),
		AlertWebhook:    SAMPLE_WEBHOOK,
		DaysAway:        1,
		ExpectedRenewal: 50,
	}

	err := common.SendRenewalNoti(notificationUser)
	if err != nil {
		t.Fatalf("failed to send renewal notification, %v", err)
	}
}

func TestStartBot_RetryLoopOnBadBalance(t *testing.T) {
	truncateTable("users", t)
	truncateTable("copytrading", t)
	truncateTable("plans", t)

	sampleUserID := "TestFixCopyTradingBalance-UserID"
	samplePlanName := "TestFixCopyTradingBalance-PlanName"
	sampleUID_1 := "uid1"
	sampleUID_2 := "uid2"
	planMaxBal := decimal.NewFromInt(2500)

	// insert sample data
	samplePlan := db.Plan{
		Name:                 samplePlanName,
		CostPerMonth:         0,
		Comment:              "",
		AllowedCTInstances:   4,
		AllowedSignInstances: 0,
		MaxCTBalance:         planMaxBal,
		DisplayableName:      "",
		IsSold:               false,
		InitialPayment:       0,
	}
	err := db.WriteRecordPlans(samplePlan, pool)
	if err != nil {
		t.Fatalf("failed to write to plans, %v", err)
	}

	// insert sample user
	sampleUser := db.User{
		UserID:           sampleUserID,
		Plan:             samplePlanName,
		RenewalTS:        time.Time{},
		AlertWebhook:     "",
		RemainingCpytBal: decimal.NewFromInt(300),
	}
	err = db.WriteRecordUsers(sampleUser, pool)
	if err != nil {
		t.Fatalf("failed to write to users, %v", err)
	}

	// insert sample bot configs
	bc := db.NewBotConfig(sampleUserID, 1) // give balance 500
	bc.BotStatus = db.BotActive
	bc.BinanceID = sampleUID_1
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}
	bot1Bal := decimal.NewFromInt(500.0)

	bc = db.NewBotConfig(sampleUserID, 2) // balance 500
	bc.BotStatus = db.BotActive
	bc.BinanceID = sampleUID_1
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}
	bot2Bal := decimal.NewFromInt(500.0)

	bc = db.NewBotConfig(sampleUserID, 3) // balance 200
	bc.BotStatus = db.BotActive
	bc.BinanceID = sampleUID_2
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}
	bot3Bal := decimal.NewFromInt(200.0)

	bc = db.NewBotConfig(sampleUserID, 4) // this is the bot that were going to 'launch' later in this test
	bc.BotStatus = db.BotInactive
	bc.Exchange = cls.Bybit
	bc.Leverage = decimal.NewFromInt(16)
	bc.BinanceID = sampleUID_1
	bc.NotifyDest = SAMPLE_WEBHOOK
	bc.APIKeyRaw = APIKEY_ENCRYPTED
	bc.APISecretRaw = APISECRET_ENCRYPTED
	bc.TestMode = true
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}

	// insert sample data
	handler.runningMons = map[string]*Monitor{
		sampleUID_1: {
			bots: map[string]*Bot{
				cls.GetBotHash(sampleUserID, 1): {
					InitialBalance: bot1Bal,
				},
				cls.GetBotHash(sampleUserID, 2): {
					InitialBalance: bot2Bal,
				},
			},
			botsMtx: sync.Mutex{},
		},
		sampleUID_2: {
			bots: map[string]*Bot{
				cls.GetBotHash(sampleUserID, 3): {
					InitialBalance: bot3Bal,
				},
			},
			botsMtx: sync.Mutex{},
		},
	}

	// send request
	req := cls.StartBotParam{
		UserID:    sampleUserID,
		BotID:     4,
		Exchange:  "bybit",
		ApiKey:    BYB_KEY,
		ApiSecret: BYB_SECRET,
		TraderUID: sampleUID_2,
		Testmode:  true,
		Leverage:  decimal.NewFromInt(16),
	}
	isServerside, err := startBot(req, &cls.LaunchingMonitorsCache{}, pool, true)
	if err == nil {
		// read users
		user, err := db.ReadRecordUsers(sampleUserID, pool)
		if err != nil {
			t.Fatalf("failed to read users, %v", err)
		}

		// check if remaining balance was changed if not, fail
		if user.RemainingCpytBal.Equal(sampleUser.RemainingCpytBal) {
			// same balance, somethings failed
			t.Fatalf("expected bal to be fixed, but bot managed to successfully launch")
		}

		return
	}
	err = fmt.Errorf("%v (isServerside: %t)", err, isServerside)

	// error occured, was it the expected err?
	re := regexp.MustCompile(
		`The sum of the balances on your running bots must equal 2500\. Your remaining balance \(\$[\d\.]+\) is too low to launch this bot \(with a balance of \$[\d\.]+\)`,
	)
	triedToFixBal := re.MatchString(err.Error())
	if !triedToFixBal {
		t.Fatalf("expected err: your remaining balance xyz is too low, got: %v", err)
	}
}

func TestFixCopytradingBalance_Success(t *testing.T) {
	truncateTable("users", t)
	truncateTable("copytrading", t)
	truncateTable("plans", t)

	userID := "userID_TestFixCopytradingBalance"
	planName := "planName_TestFixCopytradingBalance"
	sampleUID_1 := "uid1"
	sampleUID_2 := "uid2"
	planMaxBal := decimal.NewFromInt(2500)

	// insert sample data
	samplePlan := db.Plan{
		Name:                 planName,
		CostPerMonth:         0,
		Comment:              "",
		AllowedCTInstances:   4,
		AllowedSignInstances: 0,
		MaxCTBalance:         planMaxBal,
		DisplayableName:      "",
		IsSold:               false,
		InitialPayment:       0,
	}
	err := db.WriteRecordPlans(samplePlan, pool)
	if err != nil {
		t.Fatalf("failed to write to plans, %v", err)
	}

	// insert sample user
	user := db.User{
		UserID:           userID,
		Plan:             planName,
		RenewalTS:        time.Time{},
		AlertWebhook:     "",
		RemainingCpytBal: decimal.NewFromInt(300),
	}
	err = db.WriteRecordUsers(user, pool)
	if err != nil {
		t.Fatalf("failed to write to users, %v", err)
	}

	// insert sample bot configs
	bc := db.NewBotConfig(userID, 1) // give balance 500
	bc.BotStatus = db.BotActive
	bc.BinanceID = sampleUID_1
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}
	bot1Bal := decimal.NewFromInt(500)

	bc = db.NewBotConfig(userID, 2) // balance 500
	bc.BotStatus = db.BotActive
	bc.BinanceID = sampleUID_1
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}
	bot2Bal := decimal.NewFromInt(500)

	bc = db.NewBotConfig(userID, 3) // balance 200
	bc.BotStatus = db.BotActive
	bc.BinanceID = sampleUID_2
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}
	bot3Bal := decimal.NewFromInt(200)

	// create sample handler
	handler.runningMons = map[string]*Monitor{
		sampleUID_1: {
			bots: map[string]*Bot{
				cls.GetBotHash(userID, 1): {
					InitialBalance: bot1Bal,
				},
				cls.GetBotHash(userID, 2): {
					InitialBalance: bot2Bal,
				},
			},
			botsMtx: sync.Mutex{},
		},
		sampleUID_2: {
			bots: map[string]*Bot{
				cls.GetBotHash(userID, 3): {
					InitialBalance: bot3Bal,
				},
			},
			botsMtx: sync.Mutex{},
		},
	}

	// run func, check output by reading from users again
	err = fixCopyTradingBalance(userID, pool)
	if err != nil {
		t.Fatalf("failed to fix copytrading balance, %v", err)
	}

	readUser, err := db.ReadRecordUsers(userID, pool)
	if err != nil {
		t.Fatalf("failed to read from users, %v", err)
	}

	expectedBal := planMaxBal.Sub(bot1Bal).Sub(bot2Bal).Sub(bot3Bal)
	gotBal := readUser.RemainingCpytBal
	diffProportion := (gotBal.Sub(expectedBal)).Div(gotBal).Mul(OneHundred).Abs()
	if diffProportion.GreaterThan(decimal.NewFromFloat(0.01)) {
		t.Fatalf(
			"expectedBal: %s, gotBal: %s, too far apart",
			expectedBal, gotBal,
		)
	}
}

func TestExportTrades(t *testing.T) {
	truncateTable("trades", t)

	SAMPLE_CHAN_ID := "1109478822404956182"

	// write sample data to psql table
	sampleTrades := []db.Trade{
		{
			TraderUID:    "UID1",
			TradeType:    "Close",
			Symbol:       "BTCUSDT",
			Side:         "Buy",
			Qty:          13,
			UtcTimestamp: time.Now().UTC(),
			EntryPrice:   decimal.NewFromInt(500),
			MarketPrice:  30,
			Pnl:          1092,
		},
		{
			TraderUID:    "UID1",
			TradeType:    "Close",
			Symbol:       "ETHUSDT",
			Side:         "Sell",
			Qty:          2,
			UtcTimestamp: time.Now().UTC(),
			EntryPrice:   decimal.NewFromInt(319),
			MarketPrice:  500,
			Pnl:          -20000,
		},
		{
			TraderUID:    "UID2",
			TradeType:    "Decrease",
			Symbol:       "AVAXUSDT",
			Side:         "Buy",
			Qty:          100,
			UtcTimestamp: time.Now().UTC(),
			EntryPrice:   decimal.NewFromInt(500),
			MarketPrice:  30,
			Pnl:          1092,
		},
	}

	for _, trade := range sampleTrades {
		err := trade.Write(pool)
		if err != nil {
			t.Fatalf("failed to write sample data to trades table, %v", err)
		}
	}

	// run prepForExport
	filename, err := prepForExport(pool)
	if err != nil {
		t.Fatalf("failed to prep trades for export, %v", err)
	}

	// read the generated .xlsx file
	file, err := xlsx.OpenFile(filename)
	if err != nil {
		t.Fatalf("failed to open generated xlsx file: %v", err)
	}

	tradesByTrader := make(map[string][]db.Trade)
	for _, trade := range sampleTrades {
		tradesByTrader[trade.TraderUID] = append(tradesByTrader[trade.TraderUID], trade)
	}

	// validate the PnLs
	expectedPnls := map[string]float64{
		"UID1": -18908,
		"UID2": 1092,
	}

	pnlSheet, ok := file.Sheet["PnLs"]
	if !ok {
		t.Fatalf("PnLs sheet missing in generated xlsx file")
	}

	for i, row := range pnlSheet.Rows {
		if i == 0 {
			continue
		}
		trader := row.Cells[0].String()
		pnl, _ := row.Cells[1].Float()
		if expected, ok := expectedPnls[trader]; !ok || pnl != expected {
			t.Errorf("Unexpected PnL for trader %s: got %f, want %f", trader, pnl, expected)
		}
	}

	// validate the trades
	for traderUID, trades := range tradesByTrader {
		traderSheet, ok := file.Sheet[traderUID]
		if !ok {
			t.Fatalf("Sheet for TraderUID %s missing in generated xlsx file", traderUID)
		}

		for i, row := range traderSheet.Rows {
			if i == 0 {
				continue
			}

			trade := trades[i-1]

			if row.Cells[0].String() != trade.TradeType ||
				row.Cells[1].String() != trade.Symbol ||
				row.Cells[2].String() != trade.Side {
				t.Errorf("Unexpected trade data in row %d for trader %s", i, trade.TraderUID)
			}

			qty, _ := row.Cells[3].Float()
			if qty != trade.Qty {
				t.Errorf("Unexpected Qty in row %d for trader %s: got %f, want %f", i, trade.TraderUID, qty, trade.Qty)
			}

			entryFloat, err := row.Cells[4].Float()
			if err != nil {
				t.Fatalf("to cast entry cell to float, %v", err)
			}
			entryPrice := decimal.NewFromFloat(entryFloat)
			if !entryPrice.Equal(trade.EntryPrice) {
				t.Errorf("Unexpected EntryPrice in row %d for trader %s: got %s, want %s", i, trade.TraderUID, entryPrice, trade.EntryPrice)
			}

			marketPrice, _ := row.Cells[5].Float()
			if marketPrice != trade.MarketPrice {
				t.Errorf("Unexpected MarketPrice in row %d for trader %s: got %f, want %f", i, trade.TraderUID, marketPrice, trade.MarketPrice)
			}

			pnl, _ := row.Cells[6].Float()
			if pnl != trade.Pnl {
				t.Errorf("Unexpected PnL in row %d for trader %s: got %f, want %f", i, trade.TraderUID, pnl, trade.Pnl)
			}

			timestamp := row.Cells[7].String()
			if timestamp != trade.UtcTimestamp.Format(time.RFC3339) {
				t.Errorf("Unexpected Timestamp in row %d for trader %s: got %s, want %s", i, trade.TraderUID, timestamp, trade.UtcTimestamp.Format(time.RFC3339))
			}
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// launch discord bot
	devBotToken := creds["LIGHTSPEED_HELPER_DISCORD_TOKEN_DEV"]
	log.Printf("LAUNCHING DISCORD BOT FOR TESTING")

	session, err := discordgo.New("Bot " + devBotToken)
	if err != nil {
		log.Fatalf("%s : failed to create session, %v", LSBOT, err)
	}
	session.Identify.Intents = discordgo.IntentsAll

	err = session.Open()
	if err != nil {
		log.Fatalf("%s : failed to open session, %v", LSBOT, err)
	}
	log.Printf("%s : %s is now online", LSBOT, session.State.User.Username)
	defer session.Close()

	tradesFilenameChan := make(chan string)
	go func() {
		tradesFilenameChan <- filename
	}()

	// send to channel, test getTradesFiles
	err = getTradesFiles(tradesFilenameChan, session, SAMPLE_CHAN_ID)
	if err != nil {
		t.Fatalf("failed to send trades files, %v", err)
	}
}

func TestGetBinPrice(t *testing.T) {
	_, err := getBinPrice("BTCUSDT")
	if err != nil {
		t.Fatalf("failed to get bin price, %v", err)
	}
}

func TestFailLoginBadAuthKeys(t *testing.T) {
	testBot := newEmptyBot("nat", 1, cls.Bybit, "key", "secret", true)

	// run get balance func
	_, err := testBot.ExcClient.getUSDTBalance()
	if err.Error() != "10003, API key is invalid." && err.Error() != "unexpected error" {
		t.Errorf("received error not as expected, received: '%v'", err)
	}
}

func TestGetPrices(t *testing.T) {
	pricesResults := make(chan cls.PricesRes, 1)
	go getPrices("BTCUSDT", pricesResults)

	res := <-pricesResults
	if res.PanicErr != nil {
		t.Fatalf("failed to get prices, panic err - %v", res.PanicErr)
	}
	if !res.Prices[cls.Bybit].IsSupported {
		t.Fatalf("getPrices incorrectly returned binance as not supported")
	}
}

func TestGetExchangeData(t *testing.T) {
	excData, err := getExchangeSymbolData(TESTING)
	if err != nil {
		t.Errorf("failed to get exchange data, %s", err)
	}
	t.Log(excData)
}

func TestGetConfigFail(t *testing.T) {
	truncateTable("copytrading", t)

	config, err := db.ReadRecordCPYT(pool, "test-i-should-not-exist", 123)
	if err == nil || !strings.HasSuffix(err.Error(), "no rows in result set") {
		t.Fatalf(
			"expected error on reading user details, instead success - receieved config: %s\n err: %v",
			config,
			err,
		)
	}
}

func TestSendStaffAlert(t *testing.T) {
	alertMsg := "ls-core test alert"

	err := common.SendStaffAlert(alertMsg)
	if err != nil {
		t.Fatalf("failed to send staff alert, %v", err)
	}
}

func TestStartBotAndStopBot(t *testing.T) {
	truncateTable("copytrading", t)
	truncateTable("users", t)

	userID := "userID_TestStartBotAndStopBot"
	botID := 1
	sampleExc := "bybit"
	sampleBinanceUID := "C20E7A8966C0014A4AF5774DD709DC42"
	sampleLeverage := decimal.NewFromInt(16)
	sampleDiscWebhook := "https://discord.com/api/webhooks/1109478837546389524/Q-Di8Zt4wUsXIzMEiUVs-UI1t-w-2pxhID3989-vGTuTiH4jmrmi_whyMlID1EFODVnT"

	// check there are enough proxies
	firstProxyCount := proxies.Length()
	if firstProxyCount <= 0 {
		t.Fatalf("expected > 0 proxies, got: %d", firstProxyCount)
	}

	// create sample bot config
	keyEncrypted, err1 := db.Encrypt(creds["ADMIN_BYBIT_TNET_KEY"], creds["DB_ENCRYPTION_KEY"])
	secretEncrypted, err2 := db.Encrypt(creds["ADMIN_BYBIT_TNET_SECRET"], creds["DB_ENCRYPTION_KEY"])
	if err1 != nil || err2 != nil {
		t.Fatalf("failed to encrypt, %v %v", err1, err2)
	}
	bc := db.NewBotConfig(userID, botID)
	bc.APIKeyRaw = keyEncrypted
	bc.APISecretRaw = secretEncrypted
	bc.BotStatus = db.BotInactive
	bc.Exchange = sampleExc
	bc.BinanceID = sampleBinanceUID
	bc.Leverage = sampleLeverage
	bc.NotifyDest = sampleDiscWebhook

	err := db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}

	// create sample user
	sampleUser := db.User{
		UserID:           userID,
		Plan:             "TestStartBotAndStopBot",
		RenewalTS:        time.Time{},
		AlertWebhook:     "",
		RemainingCpytBal: decimal.NewFromInt(10000000), // set very large so even testnet bal is less
	}
	err = db.WriteRecordUsers(sampleUser, pool)
	if err != nil {
		t.Fatalf("failed to write sample user, %v", err)
	}

	// create and send request
	startBodyStruct := cls.StartBotParam{
		UserID:    userID,
		BotID:     botID,
		Exchange:  sampleExc,
		ApiKey:    creds["ADMIN_BYBIT_TNET_KEY"],
		ApiSecret: creds["ADMIN_BYBIT_TNET_SECRET"],
		TraderUID: sampleBinanceUID,
		Testmode:  true,
		Leverage:  sampleLeverage,
	}
	req, resp := createTestRequest(startBodyStruct, userID, t)
	lmc := cls.NewLMC()
	onStartBot(resp, req, pool, &lmc, creds["DB_ENCRYPTION_KEY"])

	// parse response
	if resp.Code != 200 {
		t.Errorf("expected 200 response, got: %d", resp.Code)
	}

	var respStruct StartBotResp
	err = json.NewDecoder(resp.Body).Decode(&respStruct)
	if err != nil {
		t.Fatalf("failed to decode onStartBot response, %v, %s", err, resp.Body.String())
	}

	gotErr := respStruct.Err
	if gotErr != "" {
		t.Errorf("expected empty response, got: %s", gotErr)
	}
	if t.Failed() {
		t.FailNow()
	}

	// check proxy count has been reduced, that activity is correct
	proxyCount := proxies.Length()
	expectedProxyCount := firstProxyCount - PROX_PER_COPY
	if proxyCount != expectedProxyCount {
		t.Fatalf("expected proxy count to be %d, got: %d", expectedProxyCount, proxyCount)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	var botstatErr error
	var readBc db.BotConfig
	for i := 1; i <= 5; i++ {
		<-ticker.C

		bc, err := db.ReadRecordCPYT(pool, userID, botID)
		if err != nil {
			botstatErr = fmt.Errorf("failed to read from copytrading, %v", err)
			continue
		}

		if bc.BotStatus != db.BotActive {
			botstatErr = fmt.Errorf("expected bot status to be %s, got: %s",
				db.BotActive, bc.BotStatus)
			continue
		}

		botstatErr = nil
		readBc = bc
		break
	}
	if botstatErr != nil {
		t.Fatal(botstatErr)
	}

	// start bot success, now test stop bot
	stopBodyStruct := cls.StopBotReq{
		UserID:    userID,
		BotID:     botID,
		TraderUID: sampleBinanceUID,
	}
	req, resp = createTestRequest(stopBodyStruct, userID, t)
	onStopBot(resp, req, pool)

	// parse response
	if resp.Code != 200 {
		t.Errorf("expected response status 200, got: %d", resp.Code)
	}

	var respStructStop StopBotResp
	err = json.NewDecoder(resp.Body).Decode(&respStructStop)
	if err != nil {
		t.Fatalf("failed to decode onStopBot response, %v, %s", err, resp.Body.String())
	}

	gotErr = respStructStop.Err
	if gotErr != "" {
		t.Errorf("expected empty response, got: %s", gotErr)
	}
	if t.Failed() {
		t.FailNow()
	}

	// check that the monitor has been stopped, that bot status is inactive
	ticker = time.NewTicker(100 * time.Millisecond)
	monitorStopped := false

	for i := 1; i <= 5; i++ {
		<-ticker.C

		_, exists := handler.getMonitor(sampleBinanceUID)
		if !exists {
			monitorStopped = true
			break
		}
	}
	if !monitorStopped {
		t.Fatalf("expected monitor to be stopped, but still exists on handler")
	}

	proxyCount = proxies.Length()
	if proxyCount != firstProxyCount {
		t.Fatalf("after stopping - expected proxy count to be %d, got: %d", firstProxyCount, proxyCount)
	}

	readBc, err = db.ReadRecordCPYT(pool, userID, botID)
	if err != nil {
		t.Fatalf("failed to read from copytrading, %v", err)
	}
	if readBc.BotStatus != db.BotInactive {
		t.Fatalf("expected bot status to be %s, got: %s", db.BotInactive, readBc.BotStatus)
	}
}

func TestHandleTraderBotDifferences(t *testing.T) {
	bot := newEmptyBot(
		"TestHandleTraderBotDifferences", 1, "bybit", "apiKey",
		"apiSecret", true,
	)
	userPositions := map[string]cls.BotPosition{
		"BuyBTCUSDT": {
			Symbol:          "BTCUSDT",
			Side:            "Buy",
			Qty:             decimal.NewFromInt(1),
			Entry:           decimal.NewFromInt(0),
			Leverage:        decimal.NewFromInt(0),
			IgnoreAddsUntil: time.Time{},
			Tp:              decimal.NewFromInt(0),
			Sl:              decimal.NewFromInt(0),
			Exchange:        "bybit",
		},
		"SellAAVEUSDT": {
			Symbol:          "AAVEUSDT",
			Side:            "Sell",
			Qty:             decimal.NewFromInt(1),
			Entry:           decimal.NewFromInt(0),
			Leverage:        decimal.NewFromInt(0),
			IgnoreAddsUntil: time.Time{},
			Tp:              decimal.NewFromInt(0),
			Sl:              decimal.NewFromInt(0),
			Exchange:        "bybit",
		},
		"BuySOLUSDT": {
			Symbol:          "SOLUSDT",
			Side:            "Buy",
			Qty:             decimal.NewFromInt(1),
			Entry:           decimal.NewFromInt(0),
			Leverage:        decimal.NewFromInt(0),
			IgnoreAddsUntil: time.Time{},
			Tp:              decimal.NewFromInt(0),
			Sl:              decimal.NewFromInt(0),
			Exchange:        "bybit",
		},
	}

	traderPositions := []cls.TraderPosition{
		{
			Symbol:     "BTCUSDT",
			Side:       "Sell",
			Qty:        1,
			Timestamp:  0,
			EntryPrice: 0,
			Pnl:        0,
			MarkPrice:  0,
		},
		{
			Symbol:     "AAVEUSDT",
			Side:       "Sell",
			Qty:        1,
			Timestamp:  0,
			EntryPrice: 0,
			Pnl:        0,
			MarkPrice:  0,
		},
		{
			Symbol:     "ETHUSDT",
			Side:       "Buy",
			Qty:        1,
			Timestamp:  0,
			EntryPrice: 0,
			Pnl:        0,
			MarkPrice:  0,
		},
		{
			Symbol:     "SOLUSDT",
			Side:       "Buy",
			Qty:        1,
			Timestamp:  0,
			EntryPrice: 0,
			Pnl:        0,
			MarkPrice:  0,
		},
	}

	bot.handleTraderBotDifferences(traderPositions, "TEST-trader", false, userPositions)

	ethIsIgnored := bot.isIgnored("ETHUSDT")
	if !ethIsIgnored {
		t.Errorf("expected eth to be ignored, got .isIgnored(): false")
	}

	btcIgnored := bot.isIgnored("BTCUSDT")
	if !btcIgnored {
		t.Errorf("expected btc to be ignored, got .isIgnored(): false")
	}

	_, trackingAAVE := bot.Positions["SellAAVEUSDT"]
	if !trackingAAVE {
		isIgnored := bot.isIgnored("AAVEUSDT")
		t.Errorf("expected AAVE to be tracked, got: not tracked, isIgnored(): %t", isIgnored)
	}

	_, trackingSOL := bot.Positions["BuySOLUSDT"]
	if !trackingSOL {
		isIgnored := bot.isIgnored("BuySOLUSDT")
		t.Errorf("expected SOL to be tracked, got: not tracked, isIgnored(): %t", isIgnored)
	}

	numPositions := len(bot.Positions)
	expectedNumPositions := 2
	if numPositions != expectedNumPositions {
		t.Errorf("expected %d positions, got: %d", expectedNumPositions, numPositions)
		t.Log(bot.Positions)
	}
}

func TestResetStartingOrStoppingToInactive_Success(t *testing.T) {
	truncateTable("copytrading", t)

	// insert sample bots
	type UserIDAndStatus struct {
		userID    string
		botStatus db.BotStatus
	}
	IDsAndStatuses := []UserIDAndStatus{
		{
			userID:    "starting",
			botStatus: db.BotStarting,
		},
		{
			userID:    "stopping",
			botStatus: db.BotStopping,
		},
		{
			userID:    "active",
			botStatus: db.BotActive,
		},
		{
			userID:    "inactive",
			botStatus: db.BotInactive,
		},
	}

	for _, userStatus := range IDsAndStatuses {
		insertSampleConfig(
			t, decimal.NewFromInt(1), userStatus.userID, userStatus.botStatus,
			"sample binance id", decimal.NewFromInt(1), SAMPLE_WEBHOOK, false, false,
		)
	}

	resetBots, err := db.ResetStartingOrStoppingToInactive(pool)
	if err != nil {
		t.Fatalf("expected nil err, got: %v", err)
	}
	expectedResetBots := 2
	if resetBots != expectedResetBots {
		t.Errorf("expected %d reset bots, got: %d", expectedResetBots, resetBots)
	}

	// confirm from sql that theres 3 inactive and 1 inactive
	var countInactive int
	var countActive int

	err = pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM copytrading WHERE bot_status = 'inactive'").Scan(&countInactive)
	if err != nil {
		t.Fatalf("error querying inactive count: %v", err)
	}
	if countInactive != 3 {
		t.Errorf("expected 3 inactive bots, got: %d", countInactive)
	}

	err = pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM copytrading WHERE bot_status = 'active'").Scan(&countActive)
	if err != nil {
		t.Fatalf("error querying active count: %v", err)
	}
	if countActive != 1 {
		t.Errorf("expected 1 active bot, got: %d", countActive)
	}
}

func TestBotStatusSetBackToInactiveAfterFailStart(t *testing.T) {
	userID := "TEST-BotStatusSetBackToInactiveAfterFailStart"
	sampleBinanceUID := "C20E7A85774DD709DC42"
	sampleLeverage := decimal.NewFromInt(16)
	botID := 1

	insertSampleConfig(
		t, decimal.NewFromInt(1), userID, db.BotInactive,
		sampleBinanceUID, sampleLeverage,
		"https://discord.com/api/webhooks/1109478837546389524/Q-Di8Zt4wUsXIzMEiUVs-UI1t-w-2pxhID3989-vGTuTiH4jmrmi_whyMlID1EFODVnT",
		false, true,
	)

	startStruct := cls.StartBotParam{
		UserID:    userID,
		BotID:     botID,
		Exchange:  "bybit",
		ApiKey:    creds["ADMIN_BYBIT_TNET_KEY"],
		ApiSecret: creds["ADMIN_BYBIT_TNET_SECRET"],
		TraderUID: sampleBinanceUID,
		Testmode:  true,
		Leverage:  sampleLeverage,
	}
	req, resp := createTestRequest(startStruct, userID, t)
	lmc := cls.NewLMC()

	go onStartBot(resp, req, pool, &lmc, creds["DB_ENCRYPTION_KEY"])

	// check its changed to starting
	allowedTime := 500 * time.Millisecond
	expectedStatus := db.BotStarting
	good, endStatus := waitForStatus(expectedStatus, allowedTime, userID, botID, t)
	if !good {
		t.Errorf("bot status did not change to (%s) within %v, instead got (%s)", expectedStatus, allowedTime, endStatus)
	}

	// check its changed to inactive
	allowedTime = 10 * time.Second
	expectedStatus = db.BotInactive
	good, endStatus = waitForStatus(expectedStatus, allowedTime, userID, botID, t)
	if !good {
		t.Errorf("bot status did not change to (%s) within %v, instead got (%s)", expectedStatus, allowedTime, endStatus)
	}

	// check resp
	if resp.Code != 400 {
		t.Errorf("expected status 400, got %d", resp.Code)
	}

	var respStruct StopBotResp
	err := json.NewDecoder(resp.Body).Decode(&respStruct)
	if err != nil {
		t.Fatalf("failed to decode onStopBot response, %v", err)
	}

	expectedErr := fmt.Sprintf("%s is a bad binance ID", sampleBinanceUID)
	if respStruct.Err != expectedErr {
		t.Fatalf("expected: %s, got: %s", expectedErr, respStruct.Err)
	}
}

func TestGetConfigSuccess(t *testing.T) {
	truncateTable("copytrading", t)

	// create sample config
	sampleKey := "myApiKey"
	sampleSecret := "myApiSecret"

	keyEncrypted, err1 := db.Encrypt(sampleKey, creds["DB_ENCRYPTION_KEY"])
	secretEncrypted, err2 := db.Encrypt(sampleSecret, creds["DB_ENCRYPTION_KEY"])
	if err1 != nil || err2 != nil {
		t.Fatalf("failed to encrypt, %v %v", err1, err2)
	}

	userID := "TEST-WriteAndReadConfig"
	botID := 1
	sampleConfig := db.BotConfig{
		UserID:               userID,
		BotID:                botID,
		BotName:              "myCoolConfig",
		BotStatus:            db.BotActive,
		APIKeyRaw:            keyEncrypted,
		APISecretRaw:         secretEncrypted,
		Exchange:             "bybit",
		BinanceID:            "C20E7A8966C0014A4AF5774DD709DC42",
		ExcSpreadDiffPercent: decimal.NewFromFloat(0.5),
		Leverage:             decimal.NewFromInt(10),
		InitialOpenPercent:   decimal.NewFromInt(3),
		MaxAddMultiplier:     decimal.NewFromInt(2),
		OpenDelay:            decimal.NewFromInt(30),
		AddDelay:             decimal.NewFromInt(-1),
		OneCoinMaxPercent:    decimal.NewFromInt(50),
		BlacklistCoins:       []string{},
		WhitelistCoins:       []string{"BTCUSDT", "ETHUSDT", "ADAUSDT"},
		AddPreventionPercent: decimal.NewFromInt(5),
		BlockAddsAboveEntry:  false,
		MaxOpenPositions:     5,
		AutoTP:               decimal.NewFromInt(30),
		AutoSL:               decimal.NewFromInt(20),
		MinTraderSize:        decimal.NewFromInt(500),
		TestMode:             true,
		NotifyDest:           "https://discord.com/api/webhooks/1109478837546389524/Q-Di8Zt4wUsXIzMEiUVs-UI1t-w-2pxhID3989-vGTuTiH4jmrmi_whyMlID1EFODVnT",
		ModeCloseOnly:        false,
		ModeInverse:          false,
		ModeNoClose:          false,
		ModeBTCTriggerPrice:  decimal.NewFromInt(-1),
		ModeNoSells:          false,
	}

	// write config
	err := db.WriteRecordCPYT(pool, sampleConfig)
	if err != nil {
		t.Fatalf("failed to write config, %v", err)
	}

	// read config
	config, err := db.ReadRecordCPYT(pool, userID, botID)
	if err != nil {
		t.Fatalf("failed to read config, %v", err)
	}

	keyDecrypted, err1 := db.Decrypt(config.APIKeyRaw, creds["DB_ENCRYPTION_KEY"])
	secretDecrypted, err2 := db.Decrypt(config.APISecretRaw, creds["DB_ENCRYPTION_KEY"])
	if err1 != nil || err2 != nil {
		t.Fatalf("failed to decrypt, %v %v", err1, err2)
	}

	sampleConfig.APIKeyRaw = nil
	sampleConfig.APISecretRaw = nil
	config.APIKeyRaw = nil
	config.APISecretRaw = nil

	if !reflect.DeepEqual(config, sampleConfig) {
		t.Errorf("read config does not match expected config.\nExpected: %+v\nGot:      %+v", sampleConfig, config)
	}

	if keyDecrypted != sampleKey || secretDecrypted != sampleSecret {
		t.Fatalf("api keys do not match or secrets do not match\ngot key %s\n got secret %s", keyEncrypted, secretEncrypted)
	}
}

func TestSendClosePositionNoti_Success(t *testing.T) {
	bot := newEmptyBot(
		"TestSendClosePositionNoti_Success", 1, "bybit", "apiKey",
		"apiSecret", true,
	)
	pnl := ClosedPNL{
		Symbol:       "BTCUSDT",
		Pnl:          -1000,
		Qty:          decimal.NewFromInt(3),
		AvgExitPrice: decimal.NewFromInt(22000),
	}
	pos := cls.BotPosition{
		Symbol:          "BTCUSDT",
		Side:            "Buy",
		Qty:             decimal.NewFromInt(3),
		Entry:           decimal.NewFromInt(21000),
		Leverage:        decimal.NewFromInt(20),
		IgnoreAddsUntil: time.Time{},
		Tp:              decimal.NewFromInt(0),
		Sl:              decimal.NewFromInt(0),
		Exchange:        "bybit",
	}
	bot.sendPosClosedNotif(
		pos, &pnl, SAMPLE_WEBHOOK, "botName",
	)
}

func TestStandardOrderSequence(t *testing.T) {
	t.Run("SetupStandardOrderSequence", func(t *testing.T) {
		truncateTable("copytrading", t)

		closePositionsToClearTestAccount(t)

		sampleLev := decimal.NewFromInt(16)
		sampleBinanceID := "sample-uid"
		insertSampleConfig(
			t, decimal.NewFromInt(1), testingUserID, db.BotActive, sampleBinanceID, sampleLev,
			SAMPLE_WEBHOOK, false, false,
		)
		sampleBot, err, isServerErr := makeBot(cls.StartBotParam{
			UserID:    testingUserID,
			BotID:     1,
			Exchange:  "bybit",
			ApiKey:    creds["ADMIN_BYBIT_TNET_KEY"],
			ApiSecret: creds["ADMIN_BYBIT_TNET_SECRET"],
			TraderUID: sampleBinanceID,
			Testmode:  true,
			Leverage:  sampleLev,
		})
		if err != nil {
			t.Fatalf("failed to create bot, isServerErr: %t, %v", isServerErr, err)
		}
		bot = sampleBot
	})
	checkSkip(t)

	t.Run("OpenLongBTCUSDT_Success", func(t *testing.T) {
		doTestOpen("Buy", "BTCUSDT", t)
	})
	checkSkip(t)

	var ethPrice Decimal
	t.Run("OpenShortETHUSDT_Success", func(t *testing.T) {
		ethPrice, _ = doTestOpen("Sell", "ETHUSDT", t)
	})
	checkSkip(t)

	chgBTCUpdate := cls.Update{
		Type:        "CHANGE",
		Symbol:      "BTCUSDT",
		Side:        "Buy",
		BIN_qty:     45,
		Timestamp:   0,
		Old_BIN_qty: 20,
		Entry:       decimal.NewFromInt(0),
		Pnl:         0,
		MarkPrice:   0,
	}
	var changeBTCUSDTUpd ChangeUpdate

	t.Run("GetIncreaseBTCUSDTChangeUpdate_Success", func(t *testing.T) {
		chUp, err := getChangeUpdate(chgBTCUpdate)
		if err != nil {
			t.Fatalf("failed to get change update, %v", err)
		}
		changeBTCUSDTUpd = chUp
	})
	checkSkip(t)

	t.Run("BuyMoreLongBTCUSDT_FailPriceTooCloseToEntry", func(t *testing.T) {
		// set the market price of bybit to whatever it was for the last update, to force
		// a no trade decision because of the market price
		ch1 := changeBTCUSDTUpd
		ch1.SymbolPrices = make(map[string]cls.Price)
		for k, v := range changeBTCUSDTUpd.SymbolPrices {
			ch1.SymbolPrices[k] = v
		}
		mockByitData := ch1.SymbolPrices[cls.Bybit]
		mockByitData.Market = bot.Positions["BuyBTCUSDT"].Entry
		ch1.SymbolPrices[cls.Bybit] = mockByitData

		// send change pos order
		didChange, errors := bot.changePosition(ch1)

		// parse result
		err := errors[0]
		isNoTradeDecision := err != nil && strings.Contains(err.Error(), "no trade decision: market price ") && strings.Contains(err.Error(), " too close to entry ")

		if didChange || !isNoTradeDecision {
			t.Fatalf("unexpected result: didChange: %t, result: %s", didChange, errors)
		}
	})
	checkSkip(t)

	t.Run("BuyMoreLongBTCUSDT_Success", func(t *testing.T) {
		ch2 := changeBTCUSDTUpd
		ch2.SymbolPrices = make(map[string]cls.Price)
		for k, v := range changeBTCUSDTUpd.SymbolPrices {
			ch2.SymbolPrices[k] = v
		}

		mockByit := ch2.SymbolPrices[cls.Bybit]
		mockByit.Market = mockByit.Market.Mul(decimal.NewFromFloat(0.9)) // must be set strictly below (1 - AddPreventionPercent)

		ch2.SymbolPrices[cls.Bybit] = mockByit

		didOrder, errors := bot.changePosition(ch2)
		onExpectedTradeSuccess(didOrder, errors, t)
	})
	checkSkip(t)

	chgETHUpdate := cls.Update{
		Type:        "CHANGE",
		Symbol:      "ETHUSDT",
		Side:        "Sell",
		BIN_qty:     35,
		Timestamp:   0,
		Old_BIN_qty: 20,
		Entry:       decimal.NewFromInt(0),
		Pnl:         0,
		MarkPrice:   0,
	}
	var changeETHUSDTUpd ChangeUpdate

	t.Run("GetIncreaseSellETHUSDT_Success", func(t *testing.T) {
		chUp, err := getChangeUpdate(chgETHUpdate)
		if err != nil {
			t.Fatalf("failed to get change update, %v", err)
		}

		chUp.SymbolPrices[cls.Bybit] = cls.Price{
			ExcName:     "bybit",
			Market:      ethPrice.Mul(decimal.NewFromFloat(0.9)),
			Err:         nil,
			IsSupported: true,
		}

		changeETHUSDTUpd = chUp
	})
	checkSkip(t)

	t.Run("BuyMoreSellETHUSDT_Success", func(t *testing.T) {
		didOrder, errors := bot.changePosition(changeETHUSDTUpd)
		onExpectedTradeSuccess(didOrder, errors, t)
	})
	checkSkip(t)

	t.Run("PartialSellETHUSDT_Success", func(t *testing.T) {
		// make change update
		chgETHUpdate2 := cls.Update{
			Type:        "CHANGE",
			Symbol:      "ETHUSDT",
			Side:        "Sell",
			BIN_qty:     10,
			Timestamp:   0,
			Old_BIN_qty: 35,
			Entry:       decimal.Zero,
			Pnl:         0,
			MarkPrice:   0,
		}
		changeETHChangeUpdate2, err := getChangeUpdate(chgETHUpdate2)
		if err != nil {
			t.Fatalf("failed to get change update, %v", err)
		}

		// place position
		didOrder, errors := bot.changePosition(changeETHChangeUpdate2)
		onExpectedTradeSuccess(didOrder, errors, t)
	})
	checkSkip(t)

	t.Run("CloseSellBTCUSDT_Success", func(t *testing.T) {
		closeBTCUSDT := cls.Update{
			Type:        "CLOSE",
			Symbol:      "BTCUSDT",
			Side:        "Buy",
			BIN_qty:     0,
			Timestamp:   0,
			Old_BIN_qty: 45,
			Entry:       decimal.Zero,
			Pnl:         0,
			MarkPrice:   0,
		}

		didOrder, errors := bot.closePosition(closeBTCUSDT, false)
		onExpectedTradeSuccess(didOrder, errors, t)
	})
	checkSkip(t)

	t.Run("CloseSellETHUSDT_Success", func(t *testing.T) {
		closeETHUSDT := cls.Update{
			Type:        "CLOSE",
			Symbol:      "ETHUSDT",
			Side:        "Sell",
			BIN_qty:     0,
			Timestamp:   0,
			Old_BIN_qty: 10,
			Entry:       decimal.Zero,
			Pnl:         0,
			MarkPrice:   0,
		}

		didOrder, errors := bot.closePosition(closeETHUSDT, false)
		onExpectedTradeSuccess(didOrder, errors, t)
	})
	checkSkip(t)

	t.Run("AssertNoActivePositions", func(t *testing.T) {
		activePositions, err := bot.ExcClient.getMyPositions()
		if err != nil {
			t.Fatalf("failed to get positions, %v", err)
		}

		if len(activePositions) > 0 {
			t.Errorf(
				"expected 0 actual positions, got %d : %v\n and thought positions: %v",
				len(activePositions),
				activePositions,
				bot.Positions,
			)
		}

		if len(bot.Positions) > 0 {
			t.Errorf(
				"expected 0 thought positions, got %d : %v\n and actual positions : %v",
				len(bot.Positions),
				bot.Positions,
				activePositions,
			)
		}
	})
}

func TestGetPNL(t *testing.T) {
	t.Run("Setup", func(t *testing.T) {
		truncateTable("copytrading", t)

		closePositionsToClearTestAccount(t)

		sampleLev := decimal.NewFromInt(16)
		sampleBinanceID := "sample-uid"
		insertSampleConfig(
			t, decimal.NewFromInt(1), testingUserID, db.BotActive, sampleBinanceID, sampleLev,
			SAMPLE_WEBHOOK, false, false,
		)
		sampleBot, err, isServerErr := makeBot(cls.StartBotParam{
			UserID:    testingUserID,
			BotID:     1,
			Exchange:  "bybit",
			ApiKey:    creds["ADMIN_BYBIT_TNET_KEY"],
			ApiSecret: creds["ADMIN_BYBIT_TNET_SECRET"],
			TraderUID: sampleBinanceID,
			Testmode:  true,
			Leverage:  sampleLev,
		})
		if err != nil {
			t.Fatalf("failed to create bot, isServerErr: %t, %v", isServerErr, err)
		}
		bot = sampleBot
	})
	checkSkip(t)

	// create closed positions in pnl list on bybit
	side := "Buy"
	symbol := "BTCUSDT"
	timeNow := time.Now().In(time.UTC).Unix()
	posHash := cls.GetPosHash(symbol, side)

	doTestOpen(side, symbol, t)

	qty := bot.Positions[posHash].Qty

	closePositionsToClearTestAccount(t)

	errChan := make(chan error, 1)
	go func() {
		_, err := bot.getPnl(symbol, timeNow, qty)
		errChan <- err
	}()

	// timeout for 60s then fail
	timeout := 60 * time.Second
	ticker := time.NewTicker(timeout)

	select {
	case <-ticker.C:
		t.Fatalf("timeout reached (%v) getting pnl", timeout)
	case err := <-errChan:
		if err != nil {
			t.Fatalf("expected err == nil getting pnl, got: %v", err)
		}
	}
}

func TestTPSL(t *testing.T) {
	sampleUserID := "TEST-TPSL"
	sampleLev := decimal.NewFromInt(16)
	sampleExcSpreadDifference := decimal.NewFromInt(5)

	sampleAutoTP := decimal.NewFromInt(15)
	sampleAutoSL := decimal.NewFromInt(25)

	sampleSide := "Buy"
	sampleSymbol := "BTCUSDT"

	t.Run("SetupBuyBTC", func(t *testing.T) {
		closePositionsToClearTestAccount(t)

		truncateTable("copytrading", t)

		insertSampleConfig(
			t, sampleExcSpreadDifference, sampleUserID, db.BotActive,
			sampleUserID, sampleLev, SAMPLE_WEBHOOK, false, false,
		)

		_, err := pool.Exec(
			context.Background(),
			`UPDATE copytrading SET auto_tp = $1, auto_sl = $2, whitelist_coins = '{}'`,
			sampleAutoTP, sampleAutoSL,
		)
		if err != nil {
			t.Fatalf("failed to set auto tp, auto_sl")
		}

		sampleBot, err, isServerErr := makeBot(cls.StartBotParam{
			UserID:    sampleUserID,
			BotID:     1,
			Exchange:  "bybit",
			ApiKey:    creds["ADMIN_BYBIT_TNET_KEY"],
			ApiSecret: creds["ADMIN_BYBIT_TNET_SECRET"],
			TraderUID: "mockBinanceID",
			Testmode:  true,
			Leverage:  sampleLev,
		})
		if err != nil {
			t.Fatalf("failed to create bot, isServerErr: %t, %v", isServerErr, err)
		}
		bot = sampleBot
	})

	t.Run("BuyBTCUSDTCheckTPSL", func(t *testing.T) {
		marketPrice, _ := doTestOpen(sampleSide, sampleSymbol, t)

		// read positions
		pos, err := bot.ExcClient.getMyPosition(sampleSymbol)
		if err != nil {
			t.Fatalf("failed to get positions from bybit, %v", err)
		}

		// check tp and sl are good
		diffToTP := (pos.Tp.Sub(marketPrice)).Div(marketPrice).Mul(OneHundred).Abs()

		tpErr := (diffToTP.Sub(sampleAutoTP)).Div(sampleAutoTP).Abs()
		if tpErr.GreaterThan(decimal.NewFromFloat(0.1)) {
			t.Fatalf(
				"bad tp for %s, expected: %s, got: %s. tpErr: %s",
				sampleSymbol, sampleAutoTP, diffToTP, tpErr,
			)
		}

		diffToSL := (pos.Sl.Sub(marketPrice)).Div(marketPrice).Mul(OneHundred).Abs()
		slErr := (diffToSL.Sub(sampleAutoSL)).Div(sampleAutoSL).Abs()
		if slErr.GreaterThan(decimal.NewFromFloat(0.1)) {
			t.Fatalf(
				"bad sl for %s, expected: %s, got: %s. slErr: %s",
				sampleSymbol, sampleAutoSL, diffToSL, slErr,
			)
		}
	})

	// TODO #29 add a test for a short in TestTPSL as well
	sampleAutoSL = decimal.NewFromInt(5)
	sampleAutoTP = decimal.NewFromInt(23)
	sampleSide = "Sell"
	sampleSymbol = "ETHUSDT"

	t.Run("SetupSellETHUSDT", func(t *testing.T) {
		_, err := pool.Exec(
			context.Background(),
			`UPDATE copytrading SET auto_tp = $1, auto_sl = $2`,
			sampleAutoTP, sampleAutoSL,
		)
		if err != nil {
			t.Fatalf("failed to write to copytrading to setup SellETHUSDT, %v", err)
		}
	})

	t.Run("SellETHUSDTCheckTPSL", func(t *testing.T) {
		marketPrice, _ := doTestOpen(sampleSide, sampleSymbol, t)

		// read positions
		pos, err := bot.ExcClient.getMyPosition(sampleSymbol)
		if err != nil {
			t.Fatalf("failed to get positions from bybit, %v", err)
		}

		// check tp and sl are good
		diffToTP := (pos.Tp.Sub(marketPrice)).Div(marketPrice).Mul(OneHundred).Abs()
		tpErr := (diffToTP.Sub(sampleAutoTP)).Div(sampleAutoTP).Abs()
		if tpErr.GreaterThan(decimal.NewFromFloat(0.1)) {
			t.Fatalf(
				"bad tp for %s, expected: %s, got: %s. tpErr: %s",
				sampleSymbol, sampleAutoTP, diffToTP, tpErr,
			)
		}

		diffToSL := (pos.Sl.Sub(marketPrice)).Div(marketPrice).Mul(OneHundred).Abs()
		slErr := (diffToSL.Sub(sampleAutoSL)).Div(sampleAutoSL).Abs()
		if slErr.GreaterThan(decimal.NewFromFloat(0.1)) {
			t.Fatalf(
				"bad sl for %s, expected: %s, got: %s. slErr: %s",
				sampleSymbol, sampleAutoSL, diffToSL, slErr,
			)
		}
	})
}

func TestFailOrderSequence(t *testing.T) {
	sampleUserID := "TestFailOrderSequence-UserID" // botID will be 1
	sampleExcSpreadDifference := decimal.NewFromInt(1)
	sampleLev := decimal.NewFromInt(16)

	t.Run("InsertSampleConfigForFailSequence", func(t *testing.T) {
		truncateTable("copytrading", t)
		insertSampleConfig(
			t, sampleExcSpreadDifference, sampleUserID, db.BotActive,
			sampleUserID, sampleLev, SAMPLE_WEBHOOK, false, false,
		)
		sampleBot, err, isServerErr := makeBot(cls.StartBotParam{
			UserID:    sampleUserID,
			BotID:     1,
			Exchange:  "bybit",
			ApiKey:    creds["ADMIN_BYBIT_TNET_KEY"],
			ApiSecret: creds["ADMIN_BYBIT_TNET_SECRET"],
			TraderUID: "mockBinanceID",
			Testmode:  true,
			Leverage:  sampleLev,
		})
		if err != nil {
			t.Fatalf("failed to create bot, isServerErr: %t, %v", isServerErr, err)
		}
		bot = sampleBot
	})

	t.Run("CloseAllPositions", func(t *testing.T) {
		doCloseAllPositions(t, false)
	})

	t.Run("FailSymbolUnsupported", func(t *testing.T) {
		unsupportedSymbolUpdate := cls.Update{
			Type:        "OPEN",
			Symbol:      "UnsupportedUSDT",
			Side:        "Buy",
			BIN_qty:     123,
			Timestamp:   0,
			Old_BIN_qty: 0,
			Entry:       decimal.Zero,
			Pnl:         0,
			MarkPrice:   0,
		}
		unsupportedSymbolOpenUpdate, err := getOpenUpdate(unsupportedSymbolUpdate)
		if err != nil {
			t.Fatalf("err getting open update, %v", err)
		}

		didOrder, errors := bot.openPosition(unsupportedSymbolOpenUpdate)
		onExpectedTradeFail(didOrder, errors, t, "no trade decision: unsupported symbol")
	})

	t.Run("FailSpreadDifferenceTooGreat", func(t *testing.T) {
		symbol := "ETHUSDT"
		side := "Buy"

		// create an open update for ethusdt
		symbolPrice, err := getBinPrice(symbol)
		if err != nil {
			t.Fatalf("failed to get bin price, %v", err)
		}

		upd := cls.Update{
			Type:        "OPEN",
			Symbol:      symbol,
			Side:        side,
			BIN_qty:     20,
			Timestamp:   float64(time.Now().Add(-500 * time.Millisecond).Unix()),
			Old_BIN_qty: 0,
			Entry:       symbolPrice,
			Pnl:         0,
			MarkPrice:   symbolPrice.InexactFloat64(),
		}

		openUpd, err := getOpenUpdate(upd)
		if err != nil {
			t.Fatalf("failed to get open update, %v", err)
		}

		// manually change the spread difference to be 1.5x what excSpreadDifference is set at
		newSpread := sampleExcSpreadDifference.Mul(decimal.NewFromFloat(1.5))
		openUpd.ExcSpreadDiffPercent[cls.Bybit] = newSpread

		// check that it fails to open
		didOpen, errs := bot.openPosition(openUpd)
		if didOpen {
			t.Fatalf("expected no open, instead bot did open. Spread difference altered to %s, max set at %s", newSpread, sampleExcSpreadDifference)
		}
		expectedNumErr := 1
		gotNumErrs := len(errs)
		if gotNumErrs != expectedNumErr {
			t.Fatalf("expected %d errs, got: %d", expectedNumErr, gotNumErrs)
			for _, err := range errs {
				t.Errorf(err.Error())
			}
			t.FailNow()
		}

		gotErr := errs[0].Error()
		isSpreadDiffNoTradeErr := strings.Contains(gotErr, "no trade decision: spread difference ") &&
			strings.Contains(gotErr, " too great")

		if !isSpreadDiffNoTradeErr {
			t.Fatalf("unexpected result: (%s)", gotErr)
		}
	})

	symbol := "BTCUSDT"
	side := "Buy"
	var binQty1 Decimal
	var binPrice1 Decimal
	t.Run("OpenSymbolForChangeTesting", func(t *testing.T) {
		binPrice1, binQty1 = doTestOpen(side, symbol, t)
	})

	// now set block_adds_above_entry to true
	// and add_prevention_percent to -1 to disable it
	tag, err := pool.Exec(
		context.Background(),
		"UPDATE copytrading SET block_adds_above_entry = $1, add_prevention_percent = $4 WHERE user_id = $2 AND bot_id = $3",
		true, sampleUserID, 1, -1,
	)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf(
			"failed to set block_adds_above_entry to true, err: (%v), rows updated: (%d)",
			err, tag.RowsAffected(),
		)
	}

	t.Run("IncreaseBuyBTCUSDT_FailAboveEntry", func(t *testing.T) {
		// create mock update
		badMarketPrice := bot.Positions[cls.GetPosHash(symbol, side)].Entry.Mul(decimal.NewFromFloat(1.00001))

		changeUpd := ChangeUpdate{
			Upd: cls.Update{
				Type:        cls.ChangeLS,
				Symbol:      symbol,
				Side:        side,
				BIN_qty:     binQty1.InexactFloat64() * 1.5,
				Timestamp:   0,
				Old_BIN_qty: binQty1.InexactFloat64(),
				Entry:       binPrice1,
				Pnl:         0,
				MarkPrice:   badMarketPrice.InexactFloat64(),
			},
			SymbolPrices: map[string]cls.Price{
				"bybit": {
					ExcName:     "bybit",
					Market:      badMarketPrice,
					Err:         nil,
					IsSupported: true,
				},
			},
			IsIncrease: true,
		}

		didOrder, results := bot.changePosition(changeUpd)

		numResults := len(results)
		if numResults != 1 {
			t.Fatalf("expected 1 result, got %d: %v", numResults, results)
		}
		if didOrder || results[0] == nil {
			t.Fatalf(
				"expected didOrder == false and non nil err, got didOrder: %t and err: %v",
				didOrder, results[0],
			)
		}

		err := results[0].Error()
		isNoTradeErr_MarketAboveEntry := strings.Contains(err, "no trade decision: ") &&
			strings.Contains(err, "for side (") &&
			strings.Contains(err, "), market price (") &&
			strings.Contains(err, ") above entry (")

		if !isNoTradeErr_MarketAboveEntry {
			t.Fatalf("got unexpected err: %v", err)
		}
	})

	// now test the same but for a short
	symbol = "ETHUSDT"
	side = "Sell"

	t.Run("OpenSymbolForChangeTesting", func(t *testing.T) {
		binPrice1, binQty1 = doTestOpen(side, symbol, t)
	})

	t.Run("IncreaseSellETHUSDT_FailAboveEntry", func(t *testing.T) {
		// create mock update
		badMarketPrice := bot.Positions[cls.GetPosHash(symbol, side)].Entry.Mul(decimal.NewFromFloat(0.99999))

		changeUpd := ChangeUpdate{
			Upd: cls.Update{
				Type:        cls.ChangeLS,
				Symbol:      symbol,
				Side:        side,
				BIN_qty:     binQty1.InexactFloat64() * 1.5,
				Timestamp:   0,
				Old_BIN_qty: binQty1.InexactFloat64(),
				Entry:       binPrice1,
				Pnl:         0,
				MarkPrice:   badMarketPrice.InexactFloat64(),
			},
			SymbolPrices: map[string]cls.Price{
				"bybit": {
					ExcName:     "bybit",
					Market:      badMarketPrice,
					Err:         nil,
					IsSupported: true,
				},
			},
			IsIncrease: true,
		}

		didOrder, results := bot.changePosition(changeUpd)

		numResults := len(results)
		if numResults != 1 {
			t.Fatalf("expected 1 result, got %d: %v", numResults, results)
		}
		if didOrder || results[0] == nil {
			t.Fatalf(
				"expected didOrder == false and non nil err, got didOrder: %t and err: %v",
				didOrder, results[0],
			)
		}

		err := results[0].Error()
		isNoTradeErr_MarketAboveEntry := strings.Contains(err, "no trade decision: ") &&
			strings.Contains(err, "for side (") &&
			strings.Contains(err, "), market price (") &&
			strings.Contains(err, ") above entry (")

		if !isNoTradeErr_MarketAboveEntry {
			t.Fatalf("got unexpected err: %v", err)
		}
	})

	t.Run("IncreaseSellETHUSDT_FailMaxAddMultiplier", func(t *testing.T) {
		maxAddMultiplier := decimal.NewFromFloat(1.5)
		initialETHPosSize := bot.Positions["SellETHUSDT"].Qty

		// update config
		_, err := pool.Exec(
			context.Background(),
			`
			UPDATE copytrading
			SET max_add_multiplier = $1
			`,
			maxAddMultiplier,
		)
		if err != nil {
			t.Fatalf("failed to update copytrading, %v", err)
		}

		// create mock update
		badMarketPrice := bot.Positions[cls.GetPosHash(symbol, side)].Entry.Mul(decimal.NewFromFloat(1.2))

		changeUpd := ChangeUpdate{
			Upd: cls.Update{
				Type:        cls.ChangeLS,
				Symbol:      symbol,
				Side:        side,
				BIN_qty:     binQty1.InexactFloat64() * (maxAddMultiplier.InexactFloat64() + 0.2),
				Timestamp:   0,
				Old_BIN_qty: binQty1.InexactFloat64(),
				Entry:       binPrice1,
				Pnl:         0,
				MarkPrice:   badMarketPrice.InexactFloat64(),
			},
			SymbolPrices: map[string]cls.Price{
				"bybit": {
					ExcName:     "bybit",
					Market:      badMarketPrice,
					Err:         nil,
					IsSupported: true,
				},
			},
			IsIncrease: true,
		}

		// change pos, check state after the change
		didOrder, results := bot.changePosition(changeUpd)

		numResults := len(results)
		if numResults != 1 {
			t.Fatalf("expected 1 result, got %d: %v", numResults, results)
		}
		if !didOrder {
			t.Errorf("expected didOrder true, got false")
		}

		// last log is Successfully ordered ...
		// 2nd last log should be the expected, 'using your max add mult...'
		numLogs := len(bot.Logs.Logs)
		expectedMsg := fmt.Sprintf(
			"Using your max add multiplier of %s",
			maxAddMultiplier,
		)
		penultmtLog := bot.Logs.Logs[numLogs-2].Msg
		if penultmtLog != expectedMsg {
			t.Errorf(
				"expected log to be %s, got: %s",
				expectedMsg, penultmtLog,
			)
		}

		postIncreaseETHPosSize := bot.Positions["SellETHUSDT"].Qty
		difference := maxAddMultiplier.Sub((postIncreaseETHPosSize.Div(initialETHPosSize)))
		if difference.Abs().GreaterThan(decimal.NewFromFloat(0.01)) {
			t.Errorf(
				"eth pos size didnt change as expected. First size: %s, second size: %s, multiplier: %s",
				initialETHPosSize, postIncreaseETHPosSize, postIncreaseETHPosSize.Div(initialETHPosSize),
			)
		}
	})
}

func TestIgnoredPositionOrderSequence(t *testing.T) {
	t.Run("Setup", func(t *testing.T) {
		truncateTable("copytrading", t)

		closePositionsToClearTestAccount(t)

		sampleLev := decimal.NewFromInt(16)
		insertSampleConfig(
			t, decimal.NewFromInt(-1), testingUserID, db.BotActive, "sample-uid",
			sampleLev, SAMPLE_WEBHOOK, false, false,
		)

		testBot, err, isServerErr := makeBot(cls.StartBotParam{
			UserID:    testingUserID,
			BotID:     1,
			Exchange:  "bybit",
			ApiKey:    BYB_KEY,
			ApiSecret: BYB_SECRET,
			TraderUID: "sample-uid",
			Testmode:  true,
			Leverage:  sampleLev,
		})
		if err != nil {
			t.Fatalf("failed to make bot, isServerErr: %t, %v", isServerErr, err)
		}
		bot = testBot
	})
	checkSkip(t)

	// ignore the position, then 'close' it
	symbol := "BTCUSDT"
	side := "Buy"
	bot.ignore(symbol)

	upd := cls.Update{
		Type:        cls.CloseLS,
		Symbol:      symbol,
		Side:        "Buy",
		BIN_qty:     0,
		Timestamp:   0,
		Old_BIN_qty: 50,
		Entry:       decimal.Zero,
		Pnl:         0,
		MarkPrice:   0,
	}

	closePositions(upd, []*Bot{bot})

	t.Run("OpenBTCUSDTAfterClosed&Ignored_Success", func(t *testing.T) {
		doTestOpen(side, symbol, t)

		pos, exists := bot.Positions[cls.GetPosHash(symbol, side)]
		if !exists {
			t.Fatalf("expected pos to exist, doesn't. All positions: %v", bot.Positions)
		}
		if pos.Qty.Equal(decimal.Zero) {
			t.Fatalf("expected non zero qty, got 0.")
		}
	})
}

func TestInverseOrderSequence(t *testing.T) {
	t.Run("SetupInverseOrderSequence", func(t *testing.T) {
		truncateTable("copytrading", t)

		closePositionsToClearTestAccount(t)

		sampleLev := decimal.NewFromInt(16)
		insertSampleConfig(
			t, decimal.NewFromInt(-1), testingUserID, db.BotActive, "sample-uid",
			sampleLev, SAMPLE_WEBHOOK, true, false,
		)
		_, err := pool.Exec(context.Background(),
			"UPDATE copytrading SET add_prevention_percent = 0.00000001")
		if err != nil {
			t.Fatalf("failed to update copytrading, %v", err)
		}

		testBot, err, isServerErr := makeBot(cls.StartBotParam{
			UserID:    testingUserID,
			BotID:     1,
			Exchange:  "bybit",
			ApiKey:    BYB_KEY,
			ApiSecret: BYB_SECRET,
			TraderUID: "sample-uid",
			Testmode:  true,
			Leverage:  sampleLev,
		})
		if err != nil {
			t.Fatalf("failed to make bot, isServerErr: %t, %v", isServerErr, err)
		}
		bot = testBot
	})
	checkSkip(t)

	// open BTC position in inverse mode (give long, but it will open a short)
	t.Run("OpenLongBTCUSDT_Success", func(t *testing.T) {
		symbol := "BTCUSDT"
		doTestOpen("Buy", symbol, t)

		pos, err := bot.ExcClient.getMyPosition(symbol)
		if err != nil {
			t.Fatalf("failed to get positions to check inverse success, %v", err)
		}
		expectedSide := "Sell"
		if pos.Side != expectedSide {
			t.Fatalf("expected side %s, got %s", expectedSide, pos.Side)
		}
	})

	// TODO #30 add a test to increase, decrease then sell the BTC position
	// Increase position
	increaseBTCUpdate := cls.Update{
		Type:        "CHANGE",
		Symbol:      "BTCUSDT",
		Side:        "Buy",
		BIN_qty:     15, // Increasing from the existing position
		Timestamp:   0,
		Old_BIN_qty: 10,
		Entry:       decimal.Zero,
		Pnl:         0,
		MarkPrice:   0,
	}
	increaseUpdate, err := getChangeUpdate(increaseBTCUpdate)
	if err != nil {
		t.Fatalf("failed to get change update for Increase, %v", err)
	}
	didOrder, errors := bot.changePosition(increaseUpdate)
	onExpectedTradeSuccess(didOrder, errors, t)

	// Decrease position
	decreaseBTCUpdate := cls.Update{
		Type:        "CHANGE",
		Symbol:      "BTCUSDT",
		Side:        "Buy",
		BIN_qty:     5, // Decreasing from the increased position
		Timestamp:   0,
		Old_BIN_qty: 15,
		Entry:       decimal.Zero,
		Pnl:         0,
		MarkPrice:   0,
	}
	decreaseUpdate, err := getChangeUpdate(decreaseBTCUpdate)
	if err != nil {
		t.Fatalf("failed to get change update for Decrease, %v", err)
	}
	didOrder, errors = bot.changePosition(decreaseUpdate)
	onExpectedTradeSuccess(didOrder, errors, t)

	// Close position
	closeBTCUpdate := cls.Update{
		Type:        "CLOSE",
		Symbol:      "BTCUSDT",
		Side:        "Buy",
		BIN_qty:     0,
		Timestamp:   0,
		Old_BIN_qty: 5,
		Entry:       decimal.Zero,
		Pnl:         0,
		MarkPrice:   0,
	}
	didOrder, errors = bot.closePosition(closeBTCUpdate, false)
	onExpectedTradeSuccess(didOrder, errors, t)

	t.Run("AssertNoActivePositions", func(t *testing.T) {
		activePositions, err := bot.ExcClient.getMyPositions()
		if err != nil {
			t.Fatalf("failed to get positions, %v", err)
		}

		if len(activePositions) > 0 {
			t.Errorf(
				"expected 0 actual positions, got %d : %v\n and thought positions: %v",
				len(activePositions),
				activePositions,
				bot.Positions,
			)
		}

		if len(bot.Positions) > 0 {
			t.Errorf(
				"expected 0 thought positions, got %d : %v\n and actual positions : %v",
				len(bot.Positions),
				bot.Positions,
				activePositions,
			)
		}
	})
}

func TestOpenOrderFromScratch_SuccessNoPanic(t *testing.T) {
	truncateTable("copytrading", t)
	closePositionsToClearTestAccount(t)

	handler.runningMons = map[string]*Monitor{}

	key, errKey := db.Encrypt(creds["ADMIN_BYBIT_TNET_KEY"], creds["DB_ENCRYPTION_KEY"])
	secret, errSecret := db.Encrypt(creds["ADMIN_BYBIT_TNET_SECRET"], creds["DB_ENCRYPTION_KEY"])
	if errKey != nil || errSecret != nil {
		t.Fatalf("failed to encrypt key and/or secret, (%v) (%v)", errKey, errSecret)
	}

	bc := db.BotConfig{
		UserID:               "auth0|64707121ae0c49ad676c1cb3",
		BotID:                1,
		BotName:              "Bybit testnet instance",
		BotStatus:            "active",
		APIKeyRaw:            key,
		APISecretRaw:         secret,
		Exchange:             "bybit",
		BinanceID:            "6408AAEEEBF0C76A3D5F0E39C64AAABA",
		ExcSpreadDiffPercent: decimal.NewFromInt(1),
		Leverage:             decimal.NewFromInt(16),
		InitialOpenPercent:   decimal.NewFromInt(1),
		MaxAddMultiplier:     decimal.NewFromInt(2),
		OpenDelay:            decimal.NewFromInt(2),
		AddDelay:             decimal.NewFromInt(-1),
		OneCoinMaxPercent:    decimal.NewFromInt(2),
		BlacklistCoins:       []string{},
		WhitelistCoins:       []string{},
		AddPreventionPercent: decimal.NewFromInt(1),
		BlockAddsAboveEntry:  false,
		MaxOpenPositions:     5,
		AutoTP:               decimal.NewFromInt(-1),
		AutoSL:               decimal.NewFromInt(-1),
		MinTraderSize:        decimal.NewFromInt(101),
		TestMode:             true,
		NotifyDest:           SAMPLE_WEBHOOK,
		ModeCloseOnly:        false,
		ModeInverse:          false,
		ModeNoClose:          false,
		ModeBTCTriggerPrice:  decimal.NewFromInt(-1),
		ModeNoSells:          false,
	}

	startBotReq := cls.StartBotParam{
		UserID:    bc.UserID,
		BotID:     bc.BotID,
		Exchange:  bc.Exchange,
		ApiKey:    creds["ADMIN_BYBIT_TNET_KEY"],
		ApiSecret: creds["ADMIN_BYBIT_TNET_SECRET"],
		TraderUID: bc.BinanceID,
		Testmode:  bc.TestMode,
		Leverage:  bc.Leverage,
	}

	err := db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}

	newMon, err := createNewMonitor(cls.GetBotHash(bc.UserID, bc.BotID), bc.BinanceID)
	if err != nil {
		t.Fatalf("failed to create a new monitor, %v", err)
	}

	bot, err, isServerErr := makeBot(startBotReq)
	if err != nil {
		t.Fatalf("failed to make bot, isServerErr: %t, %v", isServerErr, err)
	}

	newMon.addBot(bot)

	handler.addMonitor(newMon)

	// now send the update, as close as possible as it was during panic
	symbol := "KAVAUSDT"

	price, err := getBinPrice(symbol)
	if err != nil {
		t.Fatalf("failed to get price of %s, %v", symbol, err)
	}

	update := cls.Update{
		Type:        cls.OpenLS,
		Symbol:      symbol,
		Side:        "Sell",
		BIN_qty:     15897.2,
		Timestamp:   float64(time.Now().Unix()),
		Old_BIN_qty: 0,
		Entry:       price,
		Pnl:         0,
		MarkPrice:   price.InexactFloat64(),
	}
	newMon.sendAllUpdates([]cls.Update{update})

	// check that there is just one position open, and thats its in the correct symbol
	positions, err := bot.ExcClient.getMyPositions()
	if err != nil {
		t.Fatalf("failed to get positions, %v", err)
	}

	numPositions := len(positions)
	if numPositions != 1 {
		t.Errorf("expected 1 position, got: %d", numPositions)
	}

	_, exists := bot.Positions[update.Side+symbol]
	if !exists {
		t.Errorf("no position for %s", symbol)
	}

	if t.Failed() {
		t.Log(bot.Positions)
	}
}

func TestOnGetLogs(t *testing.T) {
	truncateTable("copytrading", t)

	userID := "userID-TestOnGetLogs"
	botID := 1

	binanceID := "binanceID-TestOnGetLogs"
	sampleHash := cls.GetBotHash(userID, botID)

	bc := db.NewBotConfig(userID, botID)
	bc.BinanceID = binanceID
	err := db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}

	time1 := time.Now().Add(-3 * time.Hour)
	time2 := time.Now().Add(-20 * time.Hour)
	time3 := time.Now().Add(-23 * time.Hour)

	msg1 := "Log message 1"
	msg2 := "Log message 2"
	msg3 := "Log message 3"

	// insert sample data
	handler.runningMons = map[string]*Monitor{
		binanceID: {
			bots: map[string]*Bot{
				sampleHash: {
					Logs: cls.BotLogs{
						Logs: []cls.BotLog{
							{
								Ts:  time1,
								Msg: msg1,
							},
							{
								Ts:  time2,
								Msg: msg2,
							},
							{
								Ts:  time3,
								Msg: msg3,
							},
						},
						Mtx: sync.Mutex{},
					},
				},
			},
			botsMtx: sync.Mutex{},
		},
	}

	// make test request
	bodyStruct := cls.GetBotLogsLocalReq{
		BinanceID: binanceID,
		BotHash:   sampleHash,
	}

	req, resp := createTestRequest(bodyStruct, userID, t)
	req.URL.RawQuery = fmt.Sprintf("botID=%d", botID)
	onGetLogs(resp, req, pool)

	// parse response
	var respStruct GetLogsResp
	err = json.NewDecoder(resp.Body).Decode(&respStruct)
	if err != nil {
		t.Fatalf("failed to decode json, %v", err)
	}

	if resp.Code != 200 {
		t.Errorf("expected status 200, got: %d", resp.Code)
		t.Errorf("resp err: %s", respStruct.Err)
	}

	// Check if the log messages and timestamps are as expected
	expectedLogs := []cls.BotLog{
		{Ts: time1, Msg: msg1},
		{Ts: time2, Msg: msg2},
		{Ts: time3, Msg: msg3},
	}

	for i, log := range respStruct.Logs {
		if log.Msg != expectedLogs[i].Msg {
			t.Errorf("expected msg %s, got: %s", expectedLogs[i].Msg, log.Msg)
		}

		// Convert float64 to int64
		if log.Ts != expectedLogs[i].Ts.Unix() {
			t.Errorf("expected ts %d, got: %d", expectedLogs[i].Ts.Unix(), log.Ts)
		}
	}
}

func TestStoreTrades(t *testing.T) {
	truncateTable("trades", t)

	// create sample data
	sampleUID := "UID-TestStoreTrades"
	numSells := 3 // sum of: 1. any CloseLS or 2. a ChangeLS which is a decrease
	sampleUpdates := []cls.Update{
		{
			Type:        cls.OpenLS,
			Symbol:      "BTCUSDT",
			Side:        "Buy",
			BIN_qty:     1,
			Timestamp:   1693712345,
			Old_BIN_qty: 0,
			Entry:       decimal.NewFromInt(10000),
			Pnl:         50,
			MarkPrice:   1000,
		},
		{
			Type:        cls.CloseLS,
			Symbol:      "LINKUSDT",
			Side:        "Sell",
			BIN_qty:     0,
			Timestamp:   1693712360,
			Old_BIN_qty: 0.98,
			Entry:       decimal.NewFromInt(10100),
			Pnl:         583,
			MarkPrice:   9000,
		},
		{
			Type:        cls.ChangeLS,
			Symbol:      "SOLUSDT",
			Side:        "Buy",
			BIN_qty:     1.5,
			Timestamp:   1693712370.0,
			Old_BIN_qty: 2.5,
			Entry:       decimal.NewFromInt(2100),
			Pnl:         23.1,
			MarkPrice:   1231,
		},
		{
			Type:        cls.ChangeLS,
			Symbol:      "DOGEUSDT",
			Side:        "Sell",
			BIN_qty:     5,
			Timestamp:   1693712370.0,
			Old_BIN_qty: 4,
			Entry:       decimal.NewFromInt(2100),
			Pnl:         23.1,
			MarkPrice:   1231,
		},
		{
			Type:        cls.CloseLS,
			Symbol:      "AVAXUSDT",
			Side:        "Buy",
			BIN_qty:     2.5,
			Timestamp:   1693712380,
			Old_BIN_qty: 0,
			Entry:       decimal.NewFromInt(2200),
			Pnl:         3000,
			MarkPrice:   8257,
		},
	}

	storeTrades(sampleUpdates, sampleUID, pool)

	// read all trades
	rows, err := pool.Query(
		context.Background(),
		"SELECT trader_uid, trade_type, symbol, side, qty, utc_timestamp, entry_price, market_price, pnl FROM trades",
	)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	var trades []db.Trade
	for rows.Next() {
		var trade db.Trade
		err := rows.Scan(
			&trade.TraderUID, &trade.TradeType, &trade.Symbol, &trade.Side,
			&trade.Qty, &trade.UtcTimestamp, &trade.EntryPrice,
			&trade.MarketPrice, &trade.Pnl,
		)
		if err != nil {
			t.Fatalf("Row parsing failed: %v", err)
		}
		trades = append(trades, trade)
	}

	// check that the trades list looks OK
	expNumTrades := numSells
	gotNumTrades := len(trades)
	if gotNumTrades != expNumTrades {
		t.Errorf("expected %d trades, got %d", expNumTrades, gotNumTrades)
	}

	for _, tr := range trades {
		if tr.MarketPrice == 0 || tr.MarketPrice == -1 {
			t.Errorf(
				"got market price: %f for stored trade %s",
				tr.MarketPrice, tr.Symbol,
			)
		}

		if tr.EntryPrice.Equal(decimal.Zero) {
			t.Errorf(
				"got entry price: %s for stored trade %s",
				tr.EntryPrice, tr.Symbol,
			)
		}
	}

	// print out trades if any errors
	if t.Failed() {
		t.Logf("got trades:")
		for _, tr := range trades {
			t.Log(tr)
		}
	}
}

// helper functions

func getBlankTestBot(envPath string) Bot {
	// load test.env
	var creds map[string]string
	creds, err := godotenv.Read(envPath)
	if err != nil {
		log.Fatalf("failed to read creds, %v", err)
	}

	// make test bot
	key := creds["ADMIN_BYBIT_TNET_KEY"]
	secret := creds["ADMIN_BYBIT_TNET_SECRET"]
	return newEmptyBot("nat", 1, cls.Bybit, key, secret, true)
}

func getInitedTestBot(envPath string) (*Bot, error) {
	// create bot
	bot := getBlankTestBot(envPath)
	bot.UserID = testingUserID
	bot.BotID = 1
	bot.Trader = "Test Trader"

	// set balance
	err := bot.setBalance()
	if err != nil {
		return nil, fmt.Errorf("failed to set balance, %v", err)
	}
	bot.InitialBalance = bot.CurBalance

	// get position data
	positions, err := bot.ExcClient.getMyPositions()
	if err != nil {
		return nil, fmt.Errorf("failed to get position data, %v", err)
	}
	bot.Positions = positions

	return &bot, nil
}

func getBinPrice(symbol string) (Decimal, error) {
	// make request to binance
	resp, err := http.Get(fmt.Sprintf("https://api.binance.com/api/v3/avgPrice?symbol=%s", symbol))
	if err != nil {
		log.Printf("failed to get price for %s, err: %v", symbol, err)
		return decimal.Zero, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("%s : failed to read body of response, err: %v", symbol, err)
		return decimal.Zero, err
	}

	// need to receive from binance as a string, then convert to a float
	var respJSON struct {
		Price string `json:"price"`
	}
	err = json.Unmarshal(body, &respJSON)
	if err != nil {
		return decimal.Zero, err
	}

	floatPrice, err := decimal.NewFromString(respJSON.Price)
	if err != nil {
		return decimal.Zero, fmt.Errorf("failed to parse float, %v", err)
	}
	return floatPrice, nil
}

// returns 1. the price of the symbol that was opened,
// and 2. the bin qty that the mock trader ordered
func doTestOpen(side string, symbol string, t *testing.T) (Decimal, Decimal) {
	// create update
	symbolPrice, err := getBinPrice(symbol)
	if err != nil {
		t.Fatalf("failed to get bin price, %v", err)
	}

	orderedQty := decimal.NewFromInt(20)
	upd := cls.Update{
		Type:        "OPEN",
		Symbol:      symbol,
		Side:        side,
		BIN_qty:     orderedQty.InexactFloat64(),
		Timestamp:   float64(time.Now().Add(-500 * time.Millisecond).Unix()),
		Old_BIN_qty: 0,
		Entry:       symbolPrice,
		Pnl:         0,
		MarkPrice:   symbolPrice.InexactFloat64(),
	}

	// get open update
	openUpd, err := getOpenUpdate(upd)
	if err != nil {
		t.Fatalf("failed to get open update, %v", err)
	}

	// dispatch to bot
	didOpen, errs := bot.openPosition(openUpd)
	if !didOpen {
		for _, err := range errs {
			t.Logf("failed to open, %v", err)
		}
		t.FailNow()
	}
	return symbolPrice, orderedQty
}

// makes an api request to bybit to check what they are
func doCloseAllPositions(t *testing.T, isInverse bool) {
	positions, err := bot.ExcClient.getMyPositions()
	if err != nil {
		t.Fatalf("failed to get positions, %v", err)
	}
	bot.Positions = positions

	var wg sync.WaitGroup
	for _, pos := range bot.Positions {
		wg.Add(1)
		go func(pos cls.BotPosition) {
			defer wg.Done()

			side := "Buy"
			if isInverse {
				if pos.Side == "Buy" {
					side = "Sell"
				}
			} else {
				side = pos.Side
			}

			upd := cls.Update{
				Type:        "CLOSE",
				Symbol:      pos.Symbol,
				Side:        side,
				BIN_qty:     0,
				Timestamp:   0,
				Old_BIN_qty: pos.Qty.InexactFloat64(),
				Entry:       pos.Entry,
				Pnl:         0,
				MarkPrice:   0,
			}

			didOrder, errs := bot.closePosition(upd, true)
			onExpectedTradeSuccess(didOrder, errs, t)
		}(pos)
	}

	wg.Wait()
	bot.Positions = map[string]cls.BotPosition{}
}

func onExpectedTradeSuccess(didOrder bool, errors []error, t *testing.T) {
	if !didOrder {
		t.Fatalf("expected didOrder == true, got false. errors: %v", errors)
	}
	doFail := false
	for _, err := range errors {
		if err != nil {
			t.Logf("expected nil errors, got %v", err)
			doFail = true
		}
	}
	if doFail {
		t.FailNow()
	}
}

func onExpectedTradeFail(didOrder bool, errors []error, t *testing.T, expectedErr string) {
	if didOrder {
		t.Fatalf("expected didOrder == false, got true. errors: %v", errors)
	}

	numErr := len(errors)
	if numErr >= 1 {
		if errors[0].Error() != expectedErr {
			t.Logf("expected 1 error, got %d:", numErr)
			for _, err := range errors {
				t.Log(err)
			}
			t.FailNow()
		}
		// pass otherwise

	} else {
		t.Fatalf("expected err, got none")
	}
}

func checkSkip(t *testing.T) {
	if t.Failed() {
		t.Skip("skipping rest of tests")
	}
}

// "https://discord.com/api/webhooks/1109478837546389524/Q-Di8Zt4wUsXIzMEiUVs-UI1t-w-2pxhID3989-vGTuTiH4jmrmi_whyMlID1EFODVnT"
// truncates copytrading and inserts a sample config. BotID as 1
func insertSampleConfig(
	t *testing.T,
	sampleSpreadDiff Decimal,
	sampleUserID string,
	sampleBotStatus db.BotStatus,
	sampleBinanceUID string,
	sampleLeverage Decimal,
	sampleWebhook string,
	sampleModeInverse bool,
	withUser bool,
) {
	key, errKey := db.Encrypt(BYB_KEY, creds["DB_ENCRYPTION_KEY"])
	secret, errSecret := db.Encrypt(BYB_SECRET, creds["DB_ENCRYPTION_KEY"])
	if errKey != nil || errSecret != nil {
		t.Fatalf("failed to encrypt key and/or secret, (%v) (%v)", errKey, errSecret)
	}

	config := db.BotConfig{
		UserID:               sampleUserID,
		BotID:                1,
		BotName:              "Test Bot",
		BotStatus:            sampleBotStatus,
		APIKeyRaw:            key,
		APISecretRaw:         secret,
		Exchange:             "bybit",
		BinanceID:            sampleBinanceUID,
		ExcSpreadDiffPercent: sampleSpreadDiff,
		Leverage:             sampleLeverage,
		InitialOpenPercent:   decimal.NewFromInt(3),
		MaxAddMultiplier:     decimal.NewFromInt(2),
		OpenDelay:            decimal.NewFromInt(30),
		AddDelay:             decimal.NewFromInt(-1),
		OneCoinMaxPercent:    decimal.NewFromInt(30),
		BlacklistCoins:       []string{},
		WhitelistCoins:       []string{"BTCUSDT", "ETHUSDT", "ADAUSDT"},
		AddPreventionPercent: decimal.NewFromFloat(0.05),
		BlockAddsAboveEntry:  false,
		MaxOpenPositions:     5,
		AutoTP:               decimal.NewFromFloat(30.0),
		AutoSL:               decimal.NewFromFloat(20.0),
		MinTraderSize:        decimal.NewFromInt(-1),
		TestMode:             true,
		NotifyDest:           sampleWebhook,
		ModeCloseOnly:        false,
		ModeInverse:          sampleModeInverse,
		ModeNoClose:          false,
		ModeBTCTriggerPrice:  decimal.NewFromInt(-1),
		ModeNoSells:          false,
	}

	err := db.WriteRecordCPYT(pool, config)
	if err != nil {
		t.Fatalf("failed to insert config for standard order sequence, %v", err)
	}

	if withUser {
		sampleUser := db.User{
			UserID:           sampleUserID,
			Plan:             fmt.Sprintf("%s-TEST_PLAN", sampleUserID),
			RenewalTS:        time.Time{},
			AlertWebhook:     "",
			RemainingCpytBal: decimal.NewFromInt(1000000000), // set very large so even testnet bal is less
		}
		err = db.WriteRecordUsers(sampleUser, pool)
		if err != nil {
			t.Fatalf("failed to write sample user, %v", err)
		}
	}
}

// returns whether it reached the expected in that time, and also
// what the status was at the end time (useful for debugging if it
// failed / hit timeout)
func waitForStatus(
	expectedStatus db.BotStatus,
	timeout time.Duration,
	userID string,
	botID int,
	t *testing.T,
) (bool, db.BotStatus) {
	start := time.Now()
	var returnStatus db.BotStatus
	for {
		if time.Since(start) > timeout {
			return false, returnStatus
		}

		config, err := db.ReadRecordCPYT(pool, userID, botID)
		if err != nil {
			t.Fatalf("failed to read from copytrading, %v", err)
		}
		returnStatus = config.BotStatus

		if config.BotStatus == expectedStatus {
			return true, returnStatus
		}

		time.Sleep(50 * time.Millisecond) // sleep for a short time before checking again
	}
}

func closePositionsToClearTestAccount(t *testing.T) {
	key := creds["ADMIN_BYBIT_TNET_KEY"]
	secret := creds["ADMIN_BYBIT_TNET_SECRET"]

	cli := newExcClient("bybit", true, key, secret)

	positions, err := cli.getMyPositions()
	if err != nil {
		t.Fatalf("failed to get position list, %v", err)
	}

	if len(positions) == 0 {
		return
	}

	// clear positions
	for _, pos := range positions {
		side := "Buy"
		if pos.Side == "Buy" {
			side = "Sell"
		}

		results := bot.ExcClient.placeOrder(
			pos.Symbol,
			side,
			pos.Qty,
			true,
			nil,
			nil,
		)

		for _, res := range results {
			if res.resStr != "200" {
				t.Fatalf("expected res 200, got: %s", res.resStr)
			}
		}
	}
}

func TestFindWebhookForUser(t *testing.T) {
	truncateTable("copytrading", t)

	userID := "userID-TestFindWebhookForUser"
	sampleNotifyDest := "notifyDest-TestFindWebhookForUser"

	// insert bots
	bc := db.NewBotConfig(userID, 1)
	err := db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}

	bc.BotID = 2
	bc.NotifyDest = sampleNotifyDest
	err = db.WriteRecordCPYT(pool, bc)
	if err != nil {
		t.Fatalf("failed to write to copytrading, %v", err)
	}

	// find a notify dest
	notifyDest, err := findWebhookForUser(userID, pool)
	if err != nil {
		t.Fatalf("failed to find webhook for user, %v", err)
	}
	if notifyDest != sampleNotifyDest {
		t.Errorf("expected: (%s), got: (%s)", sampleNotifyDest, notifyDest)
	}
}

func TestPaymentProcess(t *testing.T) {
	planName := TEST_PLAN_NAME
	userID := "TEST-PaymentProcess-Success"

	truncateTable("copytrading", t)
	truncateTable("users", t)
	truncateTable("plans", t)

	insertTestPlan(TEST_PLAN_NAME, false, 0, t)

	// connect to the Ethereum client
	client, err := ethclient.Dial(creds["TESTING_ETH_NETWORK_DIAL_ADDRESS"])
	if err != nil {
		t.Fatalf("failed to connect to the Ethereum client, %v", err)
	}

	/* test initials - */
	// send small payment
	qtyWei := getQtyWei(planName, false, t)
	txID := SendGethTx(client, qtyWei, t)

	// send request
	sentReqBody := cls.PaymentSentRequest{
		Plan:      planName,
		TxID:      txID,
		IsRenewal: false,
	}
	req, resp := createTestRequest(sentReqBody, userID, t)

	payments := cls.PaymentsHdlr{
		Payments: map[string]cls.Payment{},
		Mu:       sync.Mutex{},
	}

	onPaymentSent(resp, req, creds["OUR_WALLET_ADDRESS"], &payments, pool, client)

	// check response
	if resp.Code != 200 {
		t.Errorf("failed to start verification, %s", resp.Body.String())
	}

	var respJSON cls.PaymentSentResponse
	err = json.NewDecoder(resp.Body).Decode(&respJSON)
	if err != nil {
		t.Fatalf("failed to decode json, %v\njson:%s", err, resp.Body.String())
	}

	if respJSON.Err != "" {
		t.Errorf("expected no err, got %s", respJSON.Err)
	}
	if respJSON.VerificationInProgress != true {
		t.Errorf("expected VerificationInProgress == true, got false")
	}

	if t.Failed() {
		t.FailNow()
	}

	// loop checking the status
	checkStatusReqBody := CheckPaymentStatusRequest{
		Plan: planName,
		TxID: txID,
	}
	isSuccess := false
	for i := 0; i < 100; i++ {
		// send test request
		req, resp := createTestRequest(checkStatusReqBody, userID, t)
		onCheckPaymentStatus(resp, req, &payments)

		// parse body
		if resp.Code != 200 {
			t.Fatalf("status not as expected, got %d, %s", resp.Code, resp.Body.String())
		}

		var respJSON CheckPaymentStatusResponse
		err := json.NewDecoder(resp.Body).Decode(&respJSON)
		if err != nil {
			t.Fatalf("failed to decode response, %v\nbody:%s", err, resp.Body.String())
		}

		// handle response
		if respJSON.Err != "" {
			t.Fatalf("failed to check status, response had err, %s", respJSON.Err)
		}

		if respJSON.Status == "pending" {
			time.Sleep(16 * time.Second)
		} else if respJSON.Status == "success" {
			isSuccess = true
			break
		} else if respJSON.Status == "fail" {
			t.Fatalf("status fail, err: %s", respJSON.Err)
		} else if respJSON.Status == "null" {
			t.Fatalf("status null, err: %s", respJSON.Err)
		} else {
			t.Fatalf("got unexpected status %s, err: %s", respJSON.Status, respJSON.Err)
		}

	}

	if !isSuccess {
		t.Fatalf("payment timeout")
	}

	user, err := db.ReadRecordUsers(userID, pool)
	if err != nil {
		t.Fatalf("failed to read from users, %v", err)
	}
	renewalInit := user.RenewalTS

	/* now check that renewals work ok */
	// send small payment
	qtyWei = getQtyWei(planName, false, t)
	txID = SendGethTx(client, qtyWei, t)

	// send request
	sentReqBody = cls.PaymentSentRequest{
		Plan:      planName,
		TxID:      txID,
		IsRenewal: true,
	}
	req, resp = createTestRequest(sentReqBody, userID, t)

	payments = cls.PaymentsHdlr{
		Payments: map[string]cls.Payment{},
		Mu:       sync.Mutex{},
	}

	onPaymentSent(resp, req, creds["OUR_WALLET_ADDRESS"], &payments, pool, client)

	// check response
	if resp.Code != 200 {
		t.Errorf("failed to start verification, %s", resp.Body.String())
	}

	var respJSONRenewal cls.PaymentSentResponse
	err = json.NewDecoder(resp.Body).Decode(&respJSONRenewal)
	if err != nil {
		t.Fatalf("failed to decode json, %v\njson:%s", err, resp.Body.String())
	}

	if respJSONRenewal.Err != "" {
		t.Errorf("expected no err, got %s", respJSONRenewal.Err)
	}
	if respJSONRenewal.VerificationInProgress != true {
		t.Errorf("expected VerificationInProgress == true, got false")
	}

	if t.Failed() {
		t.FailNow()
	}

	// loop checking the status
	checkStatusReqBody = CheckPaymentStatusRequest{
		Plan: planName,
		TxID: txID,
	}
	isSuccess = false
	for i := 0; i < 100; i++ {
		// send test request
		req, resp := createTestRequest(checkStatusReqBody, userID, t)
		onCheckPaymentStatus(resp, req, &payments)

		// parse body
		if resp.Code != 200 {
			t.Fatalf("status not as expected, got %d, %s", resp.Code, resp.Body.String())
		}

		var respJSON CheckPaymentStatusResponse
		err := json.NewDecoder(resp.Body).Decode(&respJSON)
		if err != nil {
			t.Fatalf("failed to decode response, %v\nbody:%s", err, resp.Body.String())
		}

		// handle response
		if respJSON.Err != "" {
			t.Fatalf("failed to check status, response had err, %s", respJSON.Err)
		}

		if respJSON.Status == "pending" {
			time.Sleep(16 * time.Second)
		} else if respJSON.Status == "success" {
			isSuccess = true
			break
		} else if respJSON.Status == "fail" {
			t.Fatalf("status fail, err: %s", respJSON.Err)
		} else if respJSON.Status == "null" {
			t.Fatalf("status null, err: %s", respJSON.Err)
		} else {
			t.Fatalf("got unexpected status %s, err: %s", respJSON.Status, respJSON.Err)
		}

	}

	if !isSuccess {
		t.Fatalf("payment timeout")
	}

	// check that the renewal timestamp has increased
	user, err = db.ReadRecordUsers(userID, pool)
	if err != nil {
		t.Fatalf("failed to read from users, %v", err)
	}
	renewalNew := user.RenewalTS
	isOneMonthApart := renewalInit.AddDate(0, 1, 0).Equal(renewalNew)
	if !isOneMonthApart {
		durationApart := renewalNew.Sub(renewalInit)
		t.Fatalf(
			"expected %v to be exactly one month after %v, but instead got %v",
			renewalNew, renewalInit, durationApart,
		)
	}
}

// func TestAuthoriseJWT(t *testing.T) {
// 	// TODO #11 failing
// 	sampleUserID := "TEST-AuthoriseJWT-userID"

// 	// create sample JWT and sample request
// 	token, err := createSampleToken(sampleUserID, creds["AUTH0_RS256_SECRET"])
// 	if err != nil {
// 		t.Fatalf("failed to create sample token, %v", err)
// 	}
// 	// t.Fatalf(token)

// 	req, err := http.NewRequest("GET", "/test", nil)
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	req.Header.Set("Authorization", "Bearer "+token)

// 	// 'send' request
// 	resp := httptest.NewRecorder()
// 	v := newVerifier("inputs/JWKSprod.json")

// 	handler := v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
// 		// This is the next HandlerFunc.
// 		userID, err := extractUserID(r)
// 		if err != nil {
// 			t.Fatalf("failed to extract userID from request, %v", err)
// 		}

// 		w.WriteHeader(200)
// 		w.Write([]byte(userID))
// 	}))

// 	handler.ServeHTTP(resp, req)

// 	// check the status code is what we expect
// 	if status := resp.Code; status != http.StatusOK {
// 		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
// 	}

// 	// Check the response body is what we expect
// 	expected := sampleUserID
// 	if resp.Body.String() != expected {
// 		t.Errorf("handler returned unexpected body: got '%v'", resp.Body.String())
// 	}
// }

// leave sub as an empty string for no sub
func createTestRequest(bodyStruct any, sub string, t *testing.T) (*http.Request, *httptest.ResponseRecorder) {
	body, err := json.Marshal(bodyStruct)
	if err != nil {
		t.Fatalf("failed to marshal body, %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/url", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("failed to create request, %v", err)
	}

	if sub != "" {
		// create a new context to add a userID to the request
		claims := jwt.StandardClaims{
			Audience:  "",
			ExpiresAt: 0,
			Id:        "",
			IssuedAt:  0,
			Issuer:    "",
			NotBefore: 0,
			Subject:   sub,
		}
		ctx := context.WithValue(
			req.Context(),
			CtxKeyclaims,
			&claims,
		)
		req = req.WithContext(ctx)
	}

	return req, httptest.NewRecorder()
}

// create a sample token
func CreateSampleToken(sub string, hs256Secret string) (string, error) {
	claims := &jwt.StandardClaims{
		ExpiresAt: time.Now().Add(time.Hour * 72).Unix(),
		Issuer:    "test",
		Subject:   sub,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, err := token.SignedString([]byte(hs256Secret))
	if err != nil {
		return "", err
	}

	return ss, nil
}

// send a small amount of goerli eth. Returns txID
func SendGethTx(client *ethclient.Client, qtyWei *big.Int, t *testing.T) string {
	privateKey, err := crypto.HexToECDSA(creds["ADMIN_GOERLI_ETH_PRIVATE_KEY"])
	if err != nil {
		t.Fatalf("failed to load private key: %v", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("cannot assert type: publicKey is not of type *ecdsa.PublicKey")
	}

	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		t.Fatalf("failed to get nonce: %v", err)
	}

	gasLimit := uint64(21000) // in units
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatalf("failed to suggest gas price: %v", err)
	}
	gasPrice.Mul(gasPrice, big.NewInt(150)) // multiplying the gas amount we spend by 1.5 so it goes faster
	gasPrice.Div(gasPrice, big.NewInt(100))

	toAddress := ethcommon.HexToAddress(creds["OUR_WALLET_ADDRESS"])
	var data []byte
	tx := types.NewTransaction(nonce, toAddress, qtyWei, gasLimit, gasPrice, data)

	chainID, err := client.NetworkID(context.Background())
	if err != nil {
		t.Fatalf("failed to get chain ID: %v", err)
	}

	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		t.Fatalf("failed to sign tx: %v", err)
	}

	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		t.Fatalf("failed to send transaction: %v", err)
	}

	return signedTx.Hash().Hex()
}

func truncateTable(table string, t *testing.T) {
	_, err := pool.Exec(
		context.Background(),
		fmt.Sprintf("TRUNCATE TABLE %s;", table),
	)

	if err != nil {
		t.Fatalf("failed to truncate table %s, %v", table, err)
	}
}

func getQtyWei(planName string, isRenewal bool, t *testing.T) *big.Int {
	ethPrice, err := getEthPrice()
	if err != nil {
		t.Fatalf("failed to get eth price, %v", err)
	}

	plan, err := db.ReadPlan(planName, pool)
	if err != nil {
		t.Fatalf("failed to read from plans, %v", err)
	}

	desiredUSD := plan.CostPerMonth
	if !isRenewal {
		desiredUSD += plan.InitialPayment
	}

	desiredETH := new(big.Float).Quo(new(big.Float).SetFloat64(desiredUSD), new(big.Float).SetFloat64(ethPrice))
	desiredWei := new(big.Float).Mul(desiredETH, new(big.Float).SetFloat64(1e18))

	weiInt := new(big.Int)
	desiredWei.Int(weiInt)

	return weiInt
}

func checkSchema(rows pgx.Rows, expectedSchema map[string]struct {
	dataType   string
	isNullable string
}, dbName string,
) error {
	for rows.Next() {
		var columnName, dataType, isNullable string
		if err := rows.Scan(&columnName, &dataType, &isNullable); err != nil {
			return fmt.Errorf("%s : Failed to scan row: %v", dbName, err)
		}

		expected, ok := expectedSchema[columnName]
		if !ok {
			return fmt.Errorf("%s : unexpected column %s", dbName, columnName)
		}

		if dataType != expected.dataType {
			return fmt.Errorf("%s : invalid data type for column %s: got %s, want %s", dbName, columnName, dataType, expected.dataType)
		}

		if isNullable != expected.isNullable {
			return fmt.Errorf("%s : invalid nullability for column %s: got %s, want %s", dbName, columnName, isNullable, expected.isNullable)
		}

		// remove checked column from expected schema
		delete(expectedSchema, columnName)
	}

	// Check if there are any remaining columns in the expected schema
	if rows.Err() == nil {
		for columnName := range expectedSchema {
			return fmt.Errorf("%s : missing column %s", dbName, columnName)
		}
	}

	// Clean up
	rows.Close()

	return nil
}

// AbsDuration returns the absolute value of a time.Duration.
func AbsDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func insertTestPlan(name string, isSold bool, numCTInstances int, t *testing.T) {
	_, err := pool.Exec(
		context.Background(),
		"INSERT INTO plans (name, cost_per_month, comment, allowed_ct_instances, allowed_sign_instances, max_ct_balance, displayable_name, is_sold, initial_payment) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		name, // name
		0.1,  // cost_per_month
		"only exists for test cases to run, sending the correct eth and not bankrupting us of testnet eth", // comment
		numCTInstances, // allowed_ct_instances
		0,              // allowed_sign_instances
		0,              // max_ct_balance
		"Test Plan",    // displayable_name
		isSold,         // is_sold
		0.2,            // initial_payment
	)
	if err != nil {
		t.Fatalf("failed to insert test plan, %v", err)
	}
}
