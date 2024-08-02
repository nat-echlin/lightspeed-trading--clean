package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	cls "github.com/lightspeed-trading/app/classes"
)

// global handler that handles all mons (monitors) and http clients
type Handler struct {
	runningMons map[string]*Monitor
	monsMtx     sync.Mutex
	excData     map[string]*Exchange
}

func newHandler() Handler {
	return Handler{
		runningMons: map[string]*Monitor{},
		monsMtx:     sync.Mutex{},
		excData:     map[string]*cls.Exchange{},
	}
}

func (handler *Handler) addMonitor(monitor *Monitor) {
	handler.monsMtx.Lock()
	defer handler.monsMtx.Unlock()

	uid := monitor.trader.uid
	handler.runningMons[uid] = monitor

	log.Printf("HANDLER : added %s (%s) to monitor", uid, monitor.trader.name)
}

func (handler *Handler) removeMonitor(uid string) {
	handler.monsMtx.Lock()
	defer handler.monsMtx.Unlock()

	delete(handler.runningMons, uid)
}

func (handler *Handler) getMonitor(uid string) (*Monitor, bool) {
	handler.monsMtx.Lock()
	defer handler.monsMtx.Unlock()

	value, exists := handler.runningMons[uid]
	return value, exists
}

// runs forever, should be started with a go function
func (handler *Handler) startManualCheckingBots(pool *pgxpool.Pool) {
	for {
		numBots := 0

		var wg sync.WaitGroup
		for _, mon := range handler.runningMons {
			nb := len(mon.bots)

			wg.Add(nb)
			numBots += nb

			// create a new context for each mon
			ctx, cancel := context.WithCancel(context.Background())
			mon.manualCheckCancelFunc = cancel

			for _, bot := range mon.bots {
				go func(bot *Bot, ctx context.Context) {
					defer wg.Done()
					bot.checkManualChanges(ctx, pool)
				}(bot, ctx)
			}
		}
		wg.Wait()

		log.Printf("HANDLER : ran manual checker (%d bots)", numBots)
		time.Sleep(10 * time.Second)
	}
}
