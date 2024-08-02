package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	"github.com/lightspeed-trading/app/db"
	"github.com/shopspring/decimal"
)

// create a new, uninitialised bot. Unset attributes are: .Positions,
// .CurBalance, .Trader, .InitialBalance
func newEmptyBot(
	userID string,
	botID int,
	exchange string,
	apiKey string,
	apiSecret string,
	testmode bool,
) Bot {
	return Bot{
		UserID:         userID,
		BotID:          botID,
		Exchange:       exchange,
		Positions:      map[string]BotPosition{},
		PosMtx:         sync.Mutex{},
		ExcClient:      newExcClient(exchange, testmode, apiKey, apiSecret),
		OpenDelayUntil: time.Now(),
		CurBalance:     decimal.NewFromInt(-1),
		IgnoreList:     []string{},
		PnlWatching:    []string{},
		PnlWatchingMtx: sync.Mutex{},
		Trader:         "",
		InitialBalance: decimal.NewFromInt(-1),
		Logs: cls.BotLogs{
			Logs: []cls.BotLog{},
			Mtx:  sync.Mutex{},
		},
	}
}

// create a bot. The returned error can be shown directly to users, if it's
// a serverside error - the bool is true if its a serverside error
func makeBot(req cls.StartBotParam) (*Bot, error, bool) {
	// create a bot, set its balance
	bot := newEmptyBot(
		req.UserID, req.BotID, req.Exchange, req.ApiKey, req.ApiSecret, req.Testmode,
	)
	botHash := cls.GetBotHash(req.UserID, req.BotID)

	// TODO #31 for bybit, check that its not a unified account at launch

	// set bot balance
	err := bot.setBalance()
	if err != nil {
		needsKeys := strings.HasSuffix(err.Error(), "please set api key and secret")
		keysAreInvalid := strings.HasSuffix(err.Error(), "10003, API key is invalid.")
		if keysAreInvalid || needsKeys {
			return nil, fmt.Errorf("invalid api keys"), false
		}

		// unknown error
		log.Printf("%s : failed to set up balance, %v", botHash, err)
		return nil, errors.New("internal server error"), true
	}
	bot.InitialBalance = bot.CurBalance

	return &bot, nil, false
}

// compare user positions vs trader positions. Behaviour:
// trader OPEN user CLOSE - add to ignore list
// trader OPEN user OPEN - join in progress
// trader CLOSE user CLOSE - nothing
// trader CLOSE user OPEN - add to ignore list
// returns an error that can be shown to the user
// The list of positions given as userPositions and as botPositions,
// should only be positions which have non zero quantities.
func (bot *Bot) handleTraderBotDifferences(
	traderPositions []TraderPosition,
	traderName string,
	modeInverse bool,
	userPositions map[string]cls.BotPosition,
) error {
	if userPositions == nil {
		positions, err := bot.ExcClient.getMyPositions()
		if err != nil {
			return fmt.Errorf("failed to get positions, %v", err)
		}
		userPositions = positions
	}

	// iterate through all trader positions
	for _, traderPos := range traderPositions {
		// if its in inverse, need to switch ourSide to check properly
		ourSide := traderPos.Side
		if modeInverse {
			ourSide = cls.SwitchSide(ourSide)
		}

		posHash := cls.GetPosHash(traderPos.Symbol, ourSide)
		userPos, userHasOpen := userPositions[posHash]
		if !userHasOpen {
			// trader OPEN, user CLOSE
			log.Printf("%s : ignoring %s, trader OPEN user CLOSE", bot.getHash(), posHash)
			bot.ignore(traderPos.Symbol)
		} else {
			// trader OPEN, user OPEN
			log.Printf("%s : joining %s in progress", bot.getHash(), posHash)
			bot.Positions[posHash] = userPos
		}
	}

	// check trader CLOSE user OPEN
	for _, botPos := range userPositions {
		traderHasOpen := false
		for _, traderPos := range traderPositions {
			if botPos.Symbol == traderPos.Symbol {
				// trader OPEN user OPEN, behaviour already covered above
				traderHasOpen = true
				break
			}
		}
		if !traderHasOpen {
			// trader CLOSE user OPEN
			log.Printf("%s : ignoring %s, trader CLOSE user OPEN", bot.getHash(), botPos.GetHash())
			bot.ignore(botPos.Symbol)
		}
	}

	return nil
}

func (bot *Bot) setBalance() error {
	// get balance
	bal, err := bot.ExcClient.getUSDTBalance()
	if err != nil {
		return fmt.Errorf("failed to get balance, %v", err)
	}

	// set balance
	bot.CurBalance = bal
	log.Printf("%s : successfully set balance to %s", bot.getHash(), bal)

	return nil
}

// on receiving instruction that someone has started a new bot. Bool is whether it's
// a serverside error, if err != nil && !bool, err can be show direct to user
func startBot(
	req cls.StartBotParam,
	lmc *cls.LaunchingMonitorsCache,
	pool *pgxpool.Pool,
	doCorrectBal bool,
) (bool, error) {
	botHash := cls.GetBotHash(req.UserID, req.BotID)
	log.Printf("%s : receieved start bot request", botHash)

	// check that the config is filled out enough
	config, err := db.ReadRecordCPYT(pool, req.UserID, req.BotID)
	if err != nil {
		if strings.HasSuffix(err.Error(), "no rows in result set") {
			return false, fmt.Errorf("Bot %d does not exist", req.BotID)
		}
	}

	if config.BotStatus == db.BotActive {
		return false, errors.New("bot is already active")
	}
	if config.Exchange != cls.Bybit {
		return false, errors.New("unsupported exchange")
	}
	if config.Leverage.LessThan(decimal.NewFromInt(1)) {
		return false, errors.New("leverage must be >= 1")
	}
	if config.BinanceID == "" {
		return false, errors.New("binance ID cannot be empty")
	}
	if config.NotifyDest == "" {
		return false, errors.New("notification webhook cannot be empty")
	}
	if len(config.APIKeyRaw) == 0 {
		return false, errors.New("API key cannot be empty")
	}
	if len(config.APISecretRaw) == 0 {
		return false, errors.New("API secret cannot be empty")
	}

	// set bot activity and handle errors / early exit
	newBotActivity := db.BotStarting
	err = db.SetBotActivity(req.UserID, req.BotID, newBotActivity, pool)
	if err != nil {
		log.Printf("%s : failed to set bot activity to %s", req.UserID, newBotActivity)
	}

	startedSuccessfully := false
	defer func() {
		if !startedSuccessfully {
			newBotActivity := db.BotInactive
			err = db.SetBotActivity(req.UserID, req.BotID, newBotActivity, pool)
			if err != nil {
				log.Printf("%s : failed to set bot activity to %s", req.UserID, newBotActivity)
			}
		}
	}()

	// make bot
	bot, err, isServerErr := makeBot(req)
	if err != nil {
		return isServerErr, err
	}

	// check balance is good
	isGood, remainingBal, err := db.CheckAndReduceCpytBalance(
		req.UserID, bot.InitialBalance, pool,
	)
	if err != nil {
		return true, fmt.Errorf("failed to check and reduce copytrading balance, %v", err)
	}
	if !isGood {
		if doCorrectBal {
			err := fixCopyTradingBalance(req.UserID, pool)
			if err == nil {
				return startBot(req, lmc, pool, false)
			} else {
				go common.LogAndSendAlertF("failed to fix users copytrading balance, %v", err)
			}
		}

		maxBal := decimal.NewFromInt(-1)

		user, err := db.ReadRecordUsers(req.UserID, pool)
		if err != nil {
			log.Printf("%s : failed to read users table, %v", req.UserID, err)
		} else {
			plan, err := db.ReadPlan(user.Plan, pool)
			if err != nil {
				log.Printf("%s : failed to read plans for %s, %v", req.UserID, user.Plan, err)
			} else {
				maxBal = plan.MaxCTBalance
			}
		}

		// log, send to user
		log.Printf(
			"%s : user does not have enough remaining balance (%s) to launch this bot (%s). Max: %d",
			req.UserID, remainingBal, bot.InitialBalance, maxBal,
		)

		errMsg := fmt.Sprintf(
			"Your remaining balance ($%s) is too low to launch this bot (with a balance of $%s)",
			remainingBal, bot.InitialBalance,
		)
		if !maxBal.Equal(decimal.NewFromInt(-1)) {
			errMsg = fmt.Sprintf(
				"The sum of the balances on your running bots must equal %s. ",
				maxBal,
			) + errMsg
		}
		return false, errors.New(errMsg)
	}
	log.Printf("%s : reduced remaining balance by %s", req.UserID, bot.InitialBalance)

	needsBalanceReturnedIfError := true
	defer func() {
		if needsBalanceReturnedIfError {
			err := db.AddBackToCpytBal(req.UserID, bot.InitialBalance, pool)
			if err != nil {
				log.Printf(
					"%s : failed to return %s back to users remaining balance, %v",
					req.UserID, bot.InitialBalance, err,
				)
			} else {
				log.Printf(
					"%s : returned %s back to users remaining balance",
					req.UserID, bot.InitialBalance,
				)
			}
		}
	}()

	// work out if there's already a monitor running or not
	mon, monitorExists := handler.getMonitor(req.TraderUID)
	if !monitorExists {
		// check if another goroutine is already launching it
		inHandler := lmc.Wait(req.TraderUID)
		if !inHandler {
			defer lmc.Finished(req.TraderUID, false)

			// doesnt exist, so start a new monitor
			mon, err = createNewMonitor(botHash, req.TraderUID)
			if err != nil {
				if strings.HasSuffix(err.Error(), "bad trader uid") {
					log.Printf(
						"%s : (%s) is a bad uid, cannot create monitor",
						req.UserID, req.TraderUID,
					)
					return false, fmt.Errorf("%s is a bad binance ID", req.TraderUID)
				}

				return true, fmt.Errorf("failed to create new monitor, %v", err)
			}

		} else {
			monitorExists = true
		}
	}

	// handle differences between the traders active positions and the users
	err = bot.handleTraderBotDifferences(
		mon.trader.positions, mon.trader.name, config.ModeInverse, nil,
	)
	if err != nil {
		log.Printf("%s : failed to launch, user positions are bad, %v", botHash, err)
		return false, err
	}

	// log status, add the new bot to the monitor, run the monitor if its new
	log.Printf("%s : setup complete", botHash)
	numBot := mon.addBot(bot)
	if !monitorExists {
		handler.addMonitor(mon)
		go mon.run()
		lmc.Finished(req.TraderUID, true)
	}

	needsBalanceReturnedIfError = false
	startedSuccessfully = true
	bot.Logs.Logf("success launching bot for %s", mon.trader.name)

	go postBotLaunch(req, pool, numBot, monitorExists, mon)

	return false, nil
}

// num bot is the new number of bots on the monitor.
// monitor exists is whether the monitor already existed before this bot
// was launched.
// mon needs to be the same monitor that the bot was added to.
func postBotLaunch(
	req cls.StartBotParam,
	pool *pgxpool.Pool,
	numBot int,
	monitorExists bool,
	mon *Monitor,
) {
	botHash := cls.GetBotHash(req.UserID, req.BotID)
	log.Printf("%s : performing post bot launch activities…", botHash)

	// update bot activity
	newBotActivity := db.BotActive
	err := db.SetBotActivity(req.UserID, req.BotID, newBotActivity, pool)
	if err != nil {
		common.LogAndSendAlertF(
			"%s : failed to set bot activity to %s, %v",
			botHash, db.BotActive, err,
		)
	} else {
		log.Printf("%s : set bot activity to %s",
			botHash, newBotActivity)
	}

	// check if the monitor needs to be given more proxies
	if numBot == 1 && monitorExists {
		newProxies, err := proxies.NextGroup(PROX_PER_COPY - PROX_PER_SIGN)
		if err != nil {
			common.LogAndSendAlertF(
				"failed to add more clients to a monitor (proxy shortage?), %v",
				err,
			)
			return
		}

		newClients, err := createClients(newProxies)
		if err != nil {
			common.LogAndSendAlertF(
				"failed to add more clients to monitor (couldn't create), %v",
				err,
			)
		} else {
			mon.addClients(newClients)
		}
	}
}

// runs when someone requests to stop their bot
func stopBot(userID string, botID int, traderUID string) error {
	botHash := cls.GetBotHash(userID, botID)

	// set bot activity and handle errors / early exit
	newBotActivity := db.BotStopping
	err := db.SetBotActivity(userID, botID, newBotActivity, dbConnPool)
	if err != nil {
		log.Printf("%s : failed to set bot activity to %s", userID, newBotActivity)
	}

	stoppedSuccessfully := false
	defer func() {
		if !stoppedSuccessfully {
			newBotActivity := db.BotActive
			err = db.SetBotActivity(userID, botID, newBotActivity, dbConnPool)
			if err != nil {
				log.Printf("%s : failed to set bot activity to %s", userID, newBotActivity)
			}
		}
	}()

	// get monitor
	mon, monExists := handler.getMonitor(traderUID)
	if !monExists {
		stoppedSuccessfully = true
		log.Printf(
			"%s : potential fail to stop bot, requested traderUID %s does not exist",
			botHash, traderUID,
		)

		go func() {
			newBotActivity = db.BotInactive
			err = db.SetBotActivity(userID, botID, newBotActivity, dbConnPool)
			if err != nil {
				log.Printf("%s : failed to set bot activity to %s", userID, newBotActivity)
			}
		}()

		return nil
	}

	// get the bots bal then remove bot from monitor
	bot, botExists := mon.getBot(botHash)
	if !botExists {
		stoppedSuccessfully = true

		go func() {
			// collect all bot hashes that ARE on the monitor, so we can visually debug
			bothashList := mon.listBots()

			log.Printf(
				"%s : potential fail to stop bot, requested botHash does not exist on monitor %s. Present bots: (%s)",
				botHash, traderUID, strings.Join(bothashList, " "),
			)

			newBotActivity = db.BotInactive
			err = db.SetBotActivity(userID, botID, newBotActivity, dbConnPool)
			if err != nil {
				log.Printf("%s : failed to set bot activity to %s", botHash, newBotActivity)
			} else {
				log.Printf("%s : set bot activity to %s", botHash, newBotActivity)
			}
		}()

		return nil
	}

	bal := bot.InitialBalance
	botsRemaining := mon.removeBotByHash(botHash)

	// return success
	stoppedSuccessfully = true
	go func() {
		err = db.SetBotActivity(userID, botID, db.BotInactive, dbConnPool)
		if err != nil {
			log.Printf("%s : failed to set bot activity for botID %d to %s, %v", userID, botID, db.BotInactive, err)
		}

		// return balance
		err = db.AddBackToCpytBal(userID, bal, dbConnPool)
		if err != nil {
			log.Printf("%s : failed to add $%s back to users balance", userID, bal)
		}

		// if theres no more bots, stop the monitor
		if botsRemaining == 0 {
			log.Printf("%s : no bots left", mon.trader.name)

			var removedProxies []string
			if mon.webhookCount() == 0 {
				// tell monitor to end
				handler.removeMonitor(mon.trader.uid)

				go func() {
					mon.end <- true
				}()

				removedProxies = mon.proxies

			} else {
				// recover clients, leave it with amount: PROX_PER_DISC
				clients := mon.recoverClients(PROX_PER_COPY - PROX_PER_SIGN)
				for _, cli := range clients {
					removedProxies = append(removedProxies, cli.Proxy)
				}
			}

			// then add its proxies back into the available pool
			proxies.ReturnGroup(removedProxies)
			log.Printf("recovered %d proxies from %s", len(mon.proxies), mon.trader.name)
		}
	}()

	return nil
}

// determines and fixes a users remaining balance
func fixCopyTradingBalance(userID string, pool *pgxpool.Pool) error {
	// read copytrading for all of their bots, get all of their UIDs
	bots, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		return fmt.Errorf("failed to read all copytrading instances, %v", err)
	}

	// read users for their current remaining balance and plan
	user, err := db.ReadRecordUsers(userID, pool)
	if err != nil {
		return fmt.Errorf("failed to read from users, %v", err)
	}

	// read plans for their max balance
	plan, err := db.ReadPlan(user.Plan, pool)
	if err != nil {
		return fmt.Errorf("failed to read from plans")
	}

	botLoc := map[string][]string{}
	for _, bot := range bots {
		if bot.BotStatus != db.BotActive {
			continue // only do it for bots which are active
		}

		thisBotHash := cls.GetBotHash(bot.UserID, bot.BotID)

		botHashList, exists := botLoc[bot.BinanceID]
		if !exists {
			botLoc[bot.BinanceID] = []string{thisBotHash}
		} else {
			botLoc[bot.BinanceID] = append(botHashList, thisBotHash)
		}
	}

	// get balances from handler
	balanceData := map[string]Decimal{}

	for uid, botHashList := range botLoc {
		// get monitor
		mon, exists := handler.getMonitor(uid)
		if !exists {
			return fmt.Errorf(
				"no monitor for uid %s, from bothashes %v",
				uid, botHashList,
			)
		}

		// get all bots of monitor
		for _, botHash := range botHashList {
			bot, exists := mon.getBot(botHash)
			if !exists {
				return fmt.Errorf(
					"no bot %s on uid %s",
					botHash, uid,
				)
			}

			balanceData[botHash] = bot.InitialBalance
			log.Printf(
				"%s : found balance %s from bot %s",
				botHash, bot.InitialBalance, botHash,
			)
		}
	}

	activeTotal := decimal.NewFromInt(0)
	for _, activeBal := range balanceData {
		// activeTotal += activeBal
		activeTotal = activeTotal.Add(activeBal)
	}

	correctRemaining := plan.MaxCTBalance.Sub(activeTotal)

	// If the difference between correct remaining and actual remaining
	// is greater than 0.1%, write back to the user tables with the correct amount,
	// and send a staff alert instructing us to check and diagnose what
	// the issue was that led to it

	balDiff := user.RemainingCpytBal.Sub(correctRemaining)
	diffProportion := balDiff.Div(user.RemainingCpytBal)
	if diffProportion.Abs().GreaterThan(decimal.NewFromFloat(0.001)) {
		log.Printf(
			"%s : bal needed fixing, changing from %s to %s",
			userID, user.RemainingCpytBal, correctRemaining,
		)

		_, err = pool.Exec(
			context.Background(),
			`UPDATE users 
			SET remaining_cpyt_bal = $1
			WHERE user_id = $2`,
			correctRemaining, userID,
		)

		go func() {
			didFail := err != nil
			alertMsg := fmt.Sprintf(
				"Needed to update %s to %s from %s remaining balance, check and diagnose issues.\nDid fail: %t",
				userID, correctRemaining, user.RemainingCpytBal, didFail,
			)
			alertErr := common.SendStaffAlert(alertMsg)

			if alertErr != nil {
				log.Printf("failed to send staff alert\nfor msg: %s\n, %v", alertMsg, alertErr)
			}
		}()

		if err != nil {
			return fmt.Errorf(
				"failed to update bal of %s to %s, %v",
				userID, correctRemaining, err,
			)
		}

		log.Printf("%s : successfully fixed remaining balance", userID)

	} else {
		log.Printf(
			"%s : remaining bal needs no changes, current: %s, ideal: %s",
			userID, user.RemainingCpytBal, correctRemaining,
		)
	}

	return nil
}

// restarts a bot. does not return any errs, instead just logs them. any
// updates to the config need to be processed by psql before this runs
func restartBotAfterSettingsChange(
	userID string,
	botID int,
	oldTraderID string,
	pool *pgxpool.Pool,
	encryptionKey string,
	lmc *cls.LaunchingMonitorsCache,
) {
	// get config
	newConfig, stopBotErr := db.ReadRecordCPYT(pool, userID, botID)
	if stopBotErr != nil {
		common.LogAndSendAlertF("failed to get users new config, %v", stopBotErr)
		return
	}

	// decrypt api keys
	apiKey, err1 := db.Decrypt(newConfig.APIKeyRaw, encryptionKey)
	apiSecret, err2 := db.Decrypt(newConfig.APISecretRaw, encryptionKey)
	if err1 != nil || err2 != nil {
		common.LogAndSendAlertF("failed to decrypt api keys,\n%v\n%v", err1, err2)
		return
	}

	err := stopBot(userID, botID, oldTraderID)
	if err != nil {
		common.LogAndSendAlertF("failed to stop bot, %v", err)
		return
	}

	// send start bot req
	startReq := cls.StartBotParam{
		UserID:    userID,
		BotID:     botID,
		Exchange:  newConfig.Exchange,
		ApiKey:    apiKey,
		ApiSecret: apiSecret,
		TraderUID: newConfig.Exchange,
		Testmode:  newConfig.TestMode,
		Leverage:  newConfig.Leverage,
	}

	isServerside, err := startBot(startReq, lmc, pool, false)
	if err != nil {
		common.LogAndSendAlertF(
			"failed to send start bot req, req: %s\nerr:%v, isServerside: %v",
			startReq, err, isServerside,
		)
		return
	}
}
