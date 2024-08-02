package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"sync"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	db "github.com/lightspeed-trading/app/db"
	"github.com/shopspring/decimal"
	"gopkg.in/natefinch/lumberjack.v2"
)

var PORT = ":443"
var PROD_ADMIN_ROLE_ID = "1064536534742741033"
var PROD_GUILD_ID = "1009779177458782309"
var DEV_ADMIN_ROLE_ID = "1103311429710401586"
var DEV_GUILD_ID = "1103268349334536235"
var ENVPATH = "inputs/settings.env"
var RATELIMIT = 2500 // ratelimit for each proxy in ms, against binance
var PROX_PER_COPY = 5
var PROX_PER_SIGN = 1
var OneHundred = decimal.NewFromInt(100)

var TESTING bool
var STAFF_DISC_WH_URL string
var STAFF_RENEWAL_DISC_WH_URL string
var LIGHTSPEED_BOT_PROFILE_PICTURE_URL string
var AUTH0_API_TOKEN string
var WHOP_API_TOKEN string
var proxies cls.Proxies
var handler Handler
var dbConnPool *pgxpool.Pool
var LOCAL_PORT string    // eg "8080", not ":8080"
var SEND_TEST_PLANS bool // whether testPlan is sent to getPlans endpoint

type ClientProxy = cls.ClientProxy
type OpenUpdate = cls.OpenUpdate
type ChangeUpdate = cls.ChangeUpdate
type Proxies = cls.Proxies
type TraderPosition = cls.TraderPosition
type BotPosition = cls.BotPosition
type Exchange = cls.Exchange
type Symbol = cls.Symbol
type PricesRes = cls.PricesRes
type SpreadDiffs = cls.SpreadDiffs
type Decimal = decimal.Decimal

func main() {
	// set up logger
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	logger := &lumberjack.Logger{ // change file at 200MB, and delete after 7 days
		Filename:   "log.log",
		MaxSize:    200,        // in MB
		MaxBackups: 9999999999, // set very large to effectively disable the max simultaneous number of logfiles
		MaxAge:     7,          // days
	}

	wrt := io.MultiWriter(os.Stdout, logger)
	log.SetOutput(wrt)
	defer logger.Close()

	log.Printf("launching…")

	// get creds from settings env
	creds, err := godotenv.Read("inputs/settings.env")
	if err != nil {
		log.Fatalf("failed to read creds, %v", err)
	}
	TESTING, err := strconv.ParseBool(creds["TESTING"])
	if err != nil {
		log.Fatalf("non bool creds.TESTING %s, %v", creds["TESTING"], err)
	}
	LOCAL_PORT = creds["LOCAL_PORT_PROD"]
	if TESTING {
		LOCAL_PORT = creds["LOCAL_PORT_DEV"]
	}
	SEND_TEST_PLANS, err = strconv.ParseBool(creds["ALLOW_SEND_TEST_PLANS"])
	if err != nil {
		log.Fatalf("failed to parse ALLOW_SEND_TEST_PLANS as bool from creds, %v", err)
	}
	if err != nil {
		log.Fatalf("failed to parse EXPORT_TRADES_FREQUENCY_STR as int from creds, %v", err)
	}

	STAFF_DISC_WH_URL = creds["STAFF_DISC_WH_URL"]
	BOT_TOKEN := creds["LIGHTSPEED_HELPER_DISCORD_TOKEN_PROD"]
	AUTH0_API_TOKEN = creds["AUTH0_MANAGEMENT_API_TOKEN"]
	WHOP_API_TOKEN = creds["WHOP_API_TOKEN"]
	LIGHTSPEED_BOT_PROFILE_PICTURE_URL = creds["LIGHTSPEED_BOT_PROFILE_PICTURE_URL"]

	common.STAFF_DISC_WH_URL = STAFF_DISC_WH_URL
	common.LIGHTSPEED_BOT_PROFILE_PICTURE_URL = LIGHTSPEED_BOT_PROFILE_PICTURE_URL

	log.Printf("in testing mode: %t", TESTING)

	// read proxies.txt, create []Proxy
	prox, fileName, err := collectProxies()
	if err != nil {
		log.Fatalf("LAUNCH: failed to collect proxies, err: %v", err)
	} else {
		log.Printf("LAUNCH: successfully collected %d proxies from %s", len(prox), fileName)
		proxies = cls.Proxies{Available: prox, Mu: sync.Mutex{}}
	}

	// get database connection pool
	pool, err := db.GetConnPool(creds["DB_NAME"], creds["DB_USER"], creds["DB_PASS"], TESTING)
	if err != nil {
		log.Fatalf("failed to get conn pool, %v", err)
	}
	dbConnPool = pool

	// create handler
	handler = newHandler()
	setExchangeData()
	log.Printf("successfully set exchange data")

	// get monitors needed at startup
	resetBots, err := db.ResetStartingOrStoppingToInactive(pool)
	if err != nil {
		log.Fatalf("failed to reset starting/stopping bots, %v", err)
	}
	log.Printf("successfully set %d starting/stopping bots to the inactive state", resetBots)

	log.Printf("creating monitors needed for startup…")
	botConfigs, signalConfigs, uniqueUIDs := findStartupMonitors(pool)

	launchActiveMonitors(botConfigs, signalConfigs, uniqueUIDs, &proxies, creds["DB_ENCRYPTION_KEY"])
	log.Printf("successfully created monitors needed at startup")

	// create JWT verifier
	jwkPath := "inputs/JWKSprod.json"
	// if creds["testing"] == "true" {
	// 	jwkPath = "inputs/JWKSdev.json"
	// }
	v := NewVerifier(jwkPath)

	// create active payments handler
	activePayments := cls.PaymentsHdlr{
		Payments: map[string]cls.Payment{},
		Mu:       sync.Mutex{},
	}

	// connect to the Ethereum client
	client, err := ethclient.Dial(creds["ETH_NETWORK_DIAL_ADDRESS"])
	if err != nil {
		log.Printf("failed to connect to the Ethereum client, %v", err)
	}

	// launch services
	go handleApproachingRenewalDates(pool)

	tradesFilenameChan := make(chan string)
	go launchDiscordBot(
		BOT_TOKEN, pool, tradesFilenameChan,
		creds["EXPORTED_TRADES_DISCORD_CHANNEL_ID"],
	)

	go launchTradesExporter(tradesFilenameChan, pool)

	go launchMonitorLogger()

	go handler.startManualCheckingBots(pool)

	// listen to http requests
	lmc := cls.NewLMC()
	server := CreateServer(
		pool, creds["ADMIN_API_KEY"], creds["DB_ENCRYPTION_KEY"],
		creds["OUR_WALLET_ADDRESS"], &activePayments, client, v, &lmc,
	)
	log.Printf("listening on port %s", PORT)
	err = server.ListenAndServeTLS(
		fmt.Sprintf("/etc/letsencrypt/live/%s/fullchain.pem", creds["DOMAIN_NAME"]),
		fmt.Sprintf("/etc/letsencrypt/live/%s/privkey.pem", creds["DOMAIN_NAME"]),
	)
	if err != nil {
		log.Fatalf("err serving http, %v", err)
	}
}
