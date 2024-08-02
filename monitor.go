package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"

	dwh "github.com/nat-echlin/dwhooks"
)

type Monitor struct {
	trader                Trader
	httpClients           []ClientProxy
	clientsChan           chan []ClientProxy
	returnChan            chan int
	end                   chan bool
	proxies               []string
	bots                  map[string]*Bot // hashkey is got from getBotHash
	botsMtx               sync.Mutex
	manualCheckCancelFunc context.CancelFunc // stops the manual checker from running while positions are being opened to prevent collisions
	webhooks              map[string]string  // userID : webhook url
	whsMtx                sync.Mutex
}

// add a bot to the monitor, return new number of bots
func (mon *Monitor) addBot(bot *Bot) int {
	mon.botsMtx.Lock()
	defer mon.botsMtx.Unlock()

	mon.bots[cls.GetBotHash(bot.UserID, bot.BotID)] = bot
	bot.Trader = mon.trader.name

	return len(mon.bots)
}

// remove a bot from the monitor, return the number of bots remaining
func (mon *Monitor) removeBotByHash(botHash string) int {
	mon.botsMtx.Lock()
	defer mon.botsMtx.Unlock()

	delete(mon.bots, botHash)

	log.Printf("%s : removed bot %s", mon.trader.name, botHash)
	return len(mon.bots)
}

func (mon *Monitor) webhookCount() int {
	mon.whsMtx.Lock()
	defer mon.whsMtx.Unlock()

	return len(mon.webhooks)
}

// get a list of bot hashes that are currently running on the monitor
func (mon *Monitor) listBots() []string {
	mon.botsMtx.Lock()
	defer mon.botsMtx.Unlock()

	bothashList := []string{}
	for bothash := range mon.bots {
		bothashList = append(bothashList, bothash)
	}

	return bothashList
}

// create clients from proxy strings
func createClients(proxyStrs []string) ([]ClientProxy, error) {
	// initialise clients list
	clients := []ClientProxy{}

	// work out & define what format the proxies are in
	hostInd := -1
	userInd := -1

	splitProxy := strings.Split(proxyStrs[0], ":")

	if len(splitProxy) == 2 {
		hostInd = 0

	} else if len(splitProxy) == 4 {
		// there is a password, work out which is port and then define the rest from that
		splitProxy[3] = strings.TrimSuffix(splitProxy[3], "\r")
		portA, errA := strconv.Atoi(splitProxy[1])

		if errA == nil && portA < 65536 {
			hostInd = 0
			userInd = 2
		} else {
			portB, errB := strconv.Atoi(splitProxy[3])

			if errB == nil && portB < 65536 {
				userInd = 0
				hostInd = 2
			} else {
				log.Println("couldn't locate port, portA, portB : ", portA, portB)
				log.Println(splitProxy)
				return nil, fmt.Errorf("error parsing proxies")
			}
		}

	} else {
		log.Printf("failed to parse proxies, len(splitProxy) = %d", len(splitProxy))
		return nil, fmt.Errorf("error parsing proxies")
	}

	portInd := hostInd + 1
	passInd := userInd + 1

	// load proxies into their clients
	timeNow := time.Now()
	for _, proxyStr := range proxyStrs {

		splitProxy := strings.Split(proxyStr, ":")
		proxyURLstr := "http://" + splitProxy[hostInd] + ":" + strings.TrimSuffix(splitProxy[portInd], "\r")

		proxyUrl, err := url.Parse(proxyURLstr)
		if err != nil {
			return nil, fmt.Errorf("error at url.Parse, proxystr: %s\nerr:%v", proxyStr, err)
		}

		if userInd != -1 {
			proxyUrl.User = url.UserPassword(splitProxy[userInd], strings.TrimSuffix(splitProxy[passInd], "\r"))
		}

		client := &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyURL(proxyUrl),
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			Timeout: 5 * time.Second,
		}

		cliProx := ClientProxy{
			Client:      client,
			Proxy:       proxyStr,
			EarliestUse: timeNow,
			RateLimit:   time.Millisecond * time.Duration(RATELIMIT),
		}

		clients = append(clients, cliProx)
	}

	// return
	return clients, nil
}

// create a new monitor
func newMonitor(uid string, proxyStrs []string) (*Monitor, error) {
	// get clients from proxy strings
	clients, err := createClients(proxyStrs)
	if err != nil {
		return nil, fmt.Errorf("%s : failed to create clients, err: %v", uid, err)
	}

	// create trader
	trader := newTrader(uid)
	err = trader.getName(clients)
	if err != nil {
		return new(Monitor), err
	}

	err = trader.getInitPositions(clients)
	if err != nil {
		return new(Monitor), err
	}

	// create & return monitor
	_, cancel := context.WithCancel(context.Background())
	mon := Monitor{
		trader:                trader,
		httpClients:           clients,
		clientsChan:           make(chan []ClientProxy),
		returnChan:            make(chan int),
		end:                   make(chan bool),
		proxies:               proxyStrs,
		bots:                  make(map[string]*Bot),
		botsMtx:               sync.Mutex{},
		manualCheckCancelFunc: cancel,
	}
	return &mon, nil
}

// create a new monitor with no bots. createNewMonitor does not run() the monitor
// createNewMonitor logs errs and successes
func createNewMonitor(botHash string, uid string) (*Monitor, error) {
	// check we have enough proxies
	proxiesLeft := proxies.Length()
	warningLimit := PROX_PER_COPY * 3

	if proxiesLeft < PROX_PER_COPY {
		log.Printf("%s : not enough proxies to create a new monitor (needs %d), proxies left: %d", uid, PROX_PER_COPY, proxiesLeft)
		return nil, errors.New("not enough proxies")

	} else if proxiesLeft < warningLimit {
		log.Printf("%s : < %d proxies left, proxies left ; %d", uid, warningLimit, proxiesLeft)
		go common.SendStaffAlert(fmt.Sprintf("Low on proxies, remaining: %d", proxiesLeft))
	}
	log.Printf("%s : creating new monitor for: %s, proxies remaining: %d", botHash, uid, proxiesLeft-PROX_PER_COPY)

	// get the correct amount of proxies - skip error check, we've already checked above
	attachedProxies, _ := proxies.NextGroup(PROX_PER_COPY)

	monitor, err := newMonitor(uid, attachedProxies)
	if err != nil {
		log.Printf("%s : failed to create monitor, %v", uid, err)

		// return proxies because theyv just been taken above
		proxies.ReturnGroup(attachedProxies)

		return nil, err
	}

	// return the monitor
	return monitor, nil
}

// get num amount of clients (proxies) back from a monitor. It will stop using all of
// the ones that it returns.
// Calling it will block until it has received the clients.
func (mon *Monitor) recoverClients(num int) []ClientProxy {
	// tell it to return num, then wait for it to send them
	mon.returnChan <- num
	clients := <-mon.clientsChan

	return clients
}

// add clients to a monitor. Will block until they have been receieved
func (mon *Monitor) addClients(clients []ClientProxy) {
	mon.clientsChan <- clients
}

// converts an UpdateList into a dwh.Message
func (mon *Monitor) getUpdateMessage(updateList []cls.Update) dwh.Message {
	msg := dwh.NewMessage("")

	// set avatar url and username of the webhook message
	msg.SetAvatarURL("https://cdn.discordapp.com/attachments/996844791973290097/1110651828196102194/lightspeed.png")
	msg.SetUsername("LightSpeed Signals")

	// create an embed for each update
	for _, upd := range updateList {
		emb := dwh.NewEmbed()

		// get title, colour
		title, colour := upd.FmtTitleColour()
		emb.SetTitle(title)
		emb.SetColour(colour)

		// set description
		emb.SetDescription(mon.trader.name)

		// set url
		emb.SetUrl(
			fmt.Sprintf("https://www.binance.com/en/futures-activity/leaderboard/user?encryptedUid=%s", mon.trader.uid),
		)

		// set timestamp
		emb.SetTimestamp(int64(upd.Timestamp) / 1000)

		// set standard fields
		emb.AddField("Coin", upd.FmtCoin(), true)
		emb.AddField("Entry Price", upd.Entry.String(), true)
		emb.AddField("Amount ("+upd.Symbol+")", upd.FmtAmount(title), true)

		// add pnl, market if not an open (dont add market if its an open because we
		// already have entry which is the same as  market)
		if upd.Type != cls.OpenLS {
			emb.AddField("PnL", cls.FmtFloat(upd.Pnl), true)
			emb.AddField("Market Price", cls.FmtFloat(upd.MarkPrice), true)
		}

		// add the created embed onto the message
		msg.AddEmbed(emb)
	}

	// return the created msg
	return msg
}

// sends updates to all bots
func (mon *Monitor) sendAllUpdates(updates []cls.Update) {
	mon.botsMtx.Lock() // need to lock so that bots dont get added before 100% initialised
	botsMtxIsLocked := true
	defer func() {
		if botsMtxIsLocked {
			mon.botsMtx.Unlock()
		}
	}()

	log.Printf(
		"%s : dispatching %d updates to %d bots (%s)",
		mon.trader.name, len(updates), len(mon.bots), updates,
	)

	var wg sync.WaitGroup
	wg.Add(3) // number of uniquely structured goroutines (ie at 2: bots, webhooks)

	// send to bots
	go func() {
		defer wg.Done()

		mon.manualCheckCancelFunc()
		for _, upd := range updates {
			switch upd.Type {
			case cls.OpenLS:
				openPositions(upd, botMapToSlice(mon.bots))
			case cls.CloseLS:
				closePositions(upd, botMapToSlice(mon.bots))
			case cls.ChangeLS:
				changePositions(upd, botMapToSlice(mon.bots))
			}
		}
	}()

	// send to webhooks
	go func() {
		defer wg.Done()

		mon.sendUpdatesToWhks(updates)
	}()

	// store trades in database
	go func() {
		defer wg.Done()

		storeTrades(updates, mon.trader.uid, dbConnPool)
	}()

	mon.botsMtx.Unlock()
	botsMtxIsLocked = false
	wg.Wait()
}

func (mon *Monitor) sendUpdatesToWhks(updates []cls.Update) {
	msg := mon.getUpdateMessage(updates)

	// send to all of the webhooks
	var wg sync.WaitGroup

	for _, url := range mon.webhooks {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()

			wh := dwh.NewWebhook(url)

			// send to webhook
			status, err := wh.Send(msg)
			if err != nil || (status != 200 && status != 204) {
				log.Printf("%s : failed to send to webhook, sta: %d, err: %v", mon.trader.name, status, err)
			} else {
				log.Printf("%s : successfully sent to webhook", mon.trader.name)
			}
		}(url)
	}

	wg.Wait()
}

// get a bot from the monitor, by hash
func (mon *Monitor) getBot(botHash string) (*Bot, bool) {
	mon.botsMtx.Lock()
	defer mon.botsMtx.Unlock()

	bot, exists := mon.bots[botHash]
	return bot, exists
}

// run the bot until it is told to stop with the end chan.
// must be ran with a goroutine
func (mon *Monitor) run() {
	// run this loop forever
	log.Printf("%s : %s : now running", mon.trader.uid, mon.trader.name)
	for {
		for ind, cli := range mon.httpClients {

			// sleep until the proxy is available
			cli.SleepUntilReady()

			updates, newPositions, err := mon.trader.getUpdates(cli.Client)

			if err != nil {
				// we should only ratelimit the proxy if binance have received the request - ie,
				// all of the non-proxy errors. eg - we SHOULD ratelimit if its been 403'd
				if !strings.HasPrefix(err.Error(), "proxy") {
					mon.httpClients[ind].ResetUseTime()
				}
				log.Printf("%s : proxy %d : error getting updates; %v", mon.trader.name, ind+1, err)
				continue
			} else {
				// log.Printf("%s : proxy %d : success", mon.trader.name, ind+1)
				mon.httpClients[ind].ResetUseTime()
				mon.trader.positions = newPositions
			}

			if len(updates) > 0 {
				mon.sendAllUpdates(updates)
			}
		}

		// check to see if the monitor has been sent more clients to use,
		// or if the monitor needs to return proxies to the handler
		select {
		case clients := <-mon.clientsChan:
			// received more clients, add them to our list
			mon.httpClients = append(mon.httpClients, clients...)

		case numToReturn := <-mon.returnChan:
			// need to return clients back to handler, then remove them from our list
			mon.clientsChan <- mon.httpClients[:numToReturn]
			mon.httpClients = mon.httpClients[numToReturn:]

		case end := <-mon.end:
			// check if we need to kill the process
			if end {
				log.Printf("%s : closed monitor", mon.trader.name)
				return
			}

		default:
			// no messages, pass silently
		}
	}
}
