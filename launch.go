package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	cls "github.com/lightspeed-trading/app/classes"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lightspeed-trading/app/db"
)

// returns a []string of proxies, the filename of the proxies file, error if present
func collectProxies() ([]string, string, error) {
	var fileName string
	switch TESTING {
	case true:
		fileName = "inputs/devProxies.txt"
	case false:
		fileName = "inputs/prodProxies.txt"
	default:
		fileName = ""
	}

	data, err := os.ReadFile(fileName)
	if err != nil {
		log.Println("File reading error", err)
		return []string{}, "", err
	}

	lines := strings.Split(string(data), "\n")
	lastIndex := len(lines) - 1
	if lines[lastIndex] == "" {
		lines = lines[:lastIndex]
	}
	return lines, fileName, nil
}

// get exchange data for all exchanges
func getExchangeSymbolData(isTesting bool) (map[string]map[string]Symbol, error) {
	excData := map[string]map[string]Symbol{}

	// get exchange data for bybit
	cli := newExcClient(cls.Bybit, TESTING, "", "")
	symbolData, err := cli.getSymbolData()
	if err != nil {
		return nil, fmt.Errorf("failed to get symbol data, %v", err)
	}
	bybitData := map[string]Symbol{}

	// sort through all the symbols that bybit returns
	for _, sym := range symbolData {
		bybitData[sym.Name] = sym
	}
	excData[cls.Bybit] = bybitData

	// do for any other exchanges ...

	return excData, nil
}

// get the symbol data for a specific exchange, specific symbol
func setSpecificSymbolExchangeData(exchange string, symbol string) error {
	// get symbol data
	cli := newExcClient(exchange, TESTING, "", "")
	symbolData, err := cli.getSymbolData()
	if err != nil {
		return fmt.Errorf("failed to get symbol data, %v", err)
	}

	// sort through, get the correct symbol
	for _, sym := range symbolData {
		if sym.Name == symbol {
			handler.excData[exchange].SymbolData[sym.Name] = sym
		}
	}

	// didnt find the symbol in bybit response, return error
	return fmt.Errorf("couldnt find exchange data for %s", symbol)
}

// ran at launch, get the symbol data (min qty, step size etc for each symbol)
func setExchangeData() {
	allClients := getAllClients()

	// create dummy exchanges to store clients in
	exchanges := map[string]*cls.Exchange{}
	for _, exc := range cls.ValidExchanges {
		exchanges[exc] = cls.NewExchange(exc, allClients, map[string]cls.Symbol{})
	}
	handler.excData = exchanges

	// get symbol data for each exchange
	allExcSymbolData, err := getExchangeSymbolData(TESTING)
	if err != nil {
		log.Fatalf("failed to get symbol data, %v", err)
	}

	// add symbol data to exchanges
	for _, exc := range cls.ValidExchanges {
		exchanges[exc].SymbolData = allExcSymbolData[exc]
	}
	handler.excData = exchanges
}

// get a client for every proxy we have at the start
func getAllClients() []*http.Client {
	// get list of all client proxies
	allClientProxys, err := createClients(proxies.Available)
	if err != nil {
		panic(fmt.Errorf("failed to create clients, err: %v", err))
	}

	// get list of just the *http.Clients
	allClients := []*http.Client{}
	for _, cliPro := range allClientProxys {
		allClients = append(allClients, cliPro.Client)
	}

	return allClients
}

// determine monitors that need to be started off of the copytrading and
// signals db. Both map keys are the binance IDs, 1st return map value
// is list of bot configs, 2nd return map value is list of webhook urls,
// 3rd returned map is UIDs unique accross signals and bots
func findStartupMonitors(
	pool *pgxpool.Pool,
) (map[string][]db.BotConfig, map[string][]db.SignalConfig, map[string]struct{}) {
	// run sql select statement for copytrading table
	rows, err := pool.Query(context.Background(), "SELECT user_id, bot_id, exchange, bot_status, api_key_raw, api_secret_raw, binance_id, leverage, test_mode FROM copytrading")
	if err != nil {
		log.Fatalf("failed to run sql select statement on copytrading, %v", err)
	}

	// scan into bots slice
	var bots []db.BotConfig
	for rows.Next() {
		var bot db.BotConfig
		err := rows.Scan(&bot.UserID, &bot.BotID, &bot.Exchange, &bot.BotStatus, &bot.APIKeyRaw, &bot.APISecretRaw, &bot.BinanceID, &bot.Leverage, &bot.TestMode)
		if err != nil {
			log.Fatalf("failed to scan row in copytrading, %v", err)
		}
		bots = append(bots, bot)
	}
	if err = rows.Err(); err != nil {
		log.Fatalf("err reading rows in copytrading, %v", err)
	}

	// make map[string][]db.BotConfig
	unqiueCPYTUIDs := map[string][]db.BotConfig{}
	for _, b := range bots {
		if b.BotStatus != db.BotActive || b.BinanceID == "" {
			// skip if bot isnt active
			continue
		}

		_, alreadyExists := unqiueCPYTUIDs[b.BinanceID]
		if !alreadyExists {
			// create new
			unqiueCPYTUIDs[b.BinanceID] = []db.BotConfig{b}
		} else {
			// add to list
			unqiueCPYTUIDs[b.BinanceID] = append(unqiueCPYTUIDs[b.BinanceID], b)
		}
	}

	// run sql select statement for signals table
	rows, err = pool.Query(context.Background(), "SELECT user_id, binance_id, webhook_url FROM signals")
	if err != nil {
		log.Fatalf("failed to run sql select statement on signals, %v", err)
	}

	// scan into signals slice
	var signals []db.SignalConfig
	for rows.Next() {
		var signal db.SignalConfig
		err := rows.Scan(&signal.UserID, &signal.BinanceID, &signal.TargetURL)
		if err != nil {
			log.Fatalf("failed to scan row in signals, %v", err)
		}
		signals = append(signals, signal)
	}
	if err = rows.Err(); err != nil {
		log.Fatalf("err reading rows in signals, %v", err)
	}

	// make map map[string][]db.SignalConfig
	unqiueSIGNUIDs := map[string][]db.SignalConfig{}
	for _, s := range signals {
		if s.BinanceID == "" {
			continue
		}

		_, alreadyExists := unqiueSIGNUIDs[s.BinanceID]
		if !alreadyExists {
			// add to list
			unqiueSIGNUIDs[s.BinanceID] = append(unqiueSIGNUIDs[s.BinanceID], s)
		} else {
			// create new
			unqiueSIGNUIDs[s.BinanceID] = []db.SignalConfig{s}
		}
	}

	// get list of binance IDs unique accross both signals and copytrading
	uniqueKeysMap := make(map[string]struct{})
	for key := range unqiueCPYTUIDs {
		uniqueKeysMap[key] = struct{}{}
	}
	for key := range unqiueSIGNUIDs {
		uniqueKeysMap[key] = struct{}{}
	}

	return unqiueCPYTUIDs, unqiueSIGNUIDs, uniqueKeysMap
}

// start monitors based on signals table and copytrading table. needs
// to only start one monitor per trader. Give ALL proxies to the func
// as allProxies
func launchActiveMonitors(
	uniqueBots map[string][]db.BotConfig,
	uniqueSignals map[string][]db.SignalConfig,
	uniqueKeysMap map[string]struct{},
	allProxies *cls.Proxies,
	encryptionKey string,
) {
	// create a channel to send the new monitors into
	resChan := make(chan *Monitor, len(uniqueKeysMap))

	// asynchronously create a new monitor for each trader, send each monitor into results channel
	for uid := range uniqueKeysMap {
		go func(uid string) {
			// get proxies
			myProxies, err := allProxies.NextGroup(PROX_PER_SIGN)
			if err != nil {
				log.Fatalf("%s : failed to get proxies, %v", uid, err)
			}

			// create monitor
			newMon, err := newMonitor(uid, myProxies)
			if err != nil {
				log.Fatalf("%s : failed to init from json, err: %v", uid, err)
				return
			}

			// attach signalConfigs
			signalConfigs, hasWebhooks := uniqueSignals[uid]
			if hasWebhooks {
				urls := map[string]string{}
				for _, config := range signalConfigs {
					urls[config.UserID] = config.BinanceID
				}
				newMon.webhooks = urls
			}

			// attach and start bots to monitor
			botConfigs, hasBots := uniqueBots[uid]
			if hasBots {
				launchMultipleBots(botConfigs, newMon, encryptionKey)
			}

			resChan <- newMon
		}(uid)
	}

	// collect the monitors into the handler and run them
	for i := 0; i < len(uniqueKeysMap); i++ {
		mon := <-resChan

		// at this point all bots have PROX_PER_SIGN proxies, so determine if
		// that is correct or not. Then set the amount to be the correct amount
		// Don't need to lock/unlock mutexes here bcos they aren't running yet,
		// so nothing else is accessing them

		// load more proxies into it if it has bots
		if len(mon.bots) >= 1 {
			// get next proxies
			newProxies, err := allProxies.NextGroup(PROX_PER_COPY - PROX_PER_SIGN)
			if err != nil {
				log.Printf("failed to get next group of proxies (maybe shortage), %v", err)
			} else {
				// create clients out of those proxies
				newClients, err := createClients(newProxies)
				if err != nil {
					log.Printf("failed to add more clients to monitor (couldn't create), %v", err)
				} else {
					mon.httpClients = append(mon.httpClients, newClients...)
				}
			}
		}

		// if it has no bots and no webhooks, return its proxies and dont add it to handler
		if len(mon.bots) == 0 && len(mon.webhooks) == 0 {
			allProxies.ReturnGroup(mon.proxies)
			log.Printf("%s : not launching, no webhooks or correct bots", mon.trader.name)
			continue
		}

		// add to handler and run it
		handler.addMonitor(mon)
		go func(mon *Monitor) {
			mon.run()
		}(mon)
	}
}

// launch multiple bots from a config, for a specific monitor. Will fatal
// on an error. Monitor must have an initialised profile.
func launchMultipleBots(
	configs []db.BotConfig,
	mon *Monitor,
	encryptionKey string,
) {
	type makeBotResult struct {
		bot         *Bot
		userID      string
		botID       int
		err         error
		isServerErr bool
	}
	resChan := make(chan makeBotResult, len(configs))

	for _, config := range configs {
		go func(config db.BotConfig) {
			// decrypt api keys
			apiKey, err1 := db.Decrypt(config.APIKeyRaw, encryptionKey)
			apiSecret, err2 := db.Decrypt(config.APISecretRaw, encryptionKey)
			if err1 != nil || err2 != nil {
				log.Fatalf("failed to decrypt api keys / secrets, %v %v", err1, err2)
			}

			// make the bot
			bot, err, isServerErr := makeBot(
				cls.StartBotParam{
					UserID:    config.UserID,
					BotID:     config.BotID,
					Exchange:  config.Exchange,
					ApiKey:    apiKey,
					ApiSecret: apiSecret,
					TraderUID: mon.trader.uid,
					Testmode:  config.TestMode,
					Leverage:  config.Leverage,
				},
			)

			if bot != nil {
				// handle differences between the traders active positions and the users
				diffsErr := bot.handleTraderBotDifferences(mon.trader.positions, mon.trader.name, config.ModeInverse, nil)
				if diffsErr != nil {
					log.Printf("%s : %s : failed to launch, user positions are bad, %v", mon.trader.name, bot.getHash(), diffsErr)
					bot = nil
				}

				bot.Trader = mon.trader.name
			}

			// send to chan
			resChan <- makeBotResult{
				bot:         bot,
				userID:      config.UserID,
				botID:       config.BotID,
				err:         err,
				isServerErr: isServerErr,
			}
		}(config)
	}

	// collect results from chan
	log.Printf("%s : collecting results from bot creations…", mon.trader.name)
	bots := map[string]*Bot{}

	for i := 0; i < len(configs); i++ {
		res := <-resChan

		if res.isServerErr {
			log.Fatalf("%s : failed to launch bots, %v", mon.trader.name, res.err)
		}

		if res.err != nil {
			log.Printf(
				"%s : %s_%d : failed to launch bot, %v",
				mon.trader.name, res.userID, res.botID, res.err,
			)
		}

		if res.bot != nil {
			bots[cls.GetBotHash(res.userID, res.botID)] = res.bot
		}
	}

	// add to monitor
	mon.bots = bots
}

// needs to be launched as a go function
func launchMonitorLogger() {
	log.Printf("launched monitor logger")
	isLocked := false
	defer func() {
		if isLocked {
			handler.monsMtx.Unlock()
		}
	}()

	for {
		handler.monsMtx.Lock()
		isLocked = true

		monListStr := ""
		for _, mon := range handler.runningMons {
			monListStr += mon.trader.name + " "
		}

		handler.monsMtx.Unlock()
		isLocked = false

		monListStr = strings.TrimSuffix(monListStr, " ")
		log.Printf("currently running monitors: (%s)", monListStr)

		time.Sleep(1 * time.Minute)
	}
}
