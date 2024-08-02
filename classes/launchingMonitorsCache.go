package cls

// keeps track of monitors that are launching, to prevent two monitors
// being launched for the same trader at the same time. Issue can arise
// because there is a decent gap between a monitor initialisation starting,
// and that monitor being added to the handler.

import "sync"

// keep track of monitors that are currently launching
type LaunchingMonitorsCache struct {
	launching map[string]*launchingMon
	uidsMtx   sync.Mutex
}

type launchingMon struct {
	waiting []chan bool
	mtx     sync.Mutex
}

func NewLMC() LaunchingMonitorsCache {
	return LaunchingMonitorsCache{
		launching: map[string]*launchingMon{},
		uidsMtx:   sync.Mutex{},
	}
}

// wait for a monitor to be launched, return true if its now in handler, false otherwise
// eg would return false if the process errored while launching it
func (lmc *LaunchingMonitorsCache) Wait(uid string) bool {
	lmc.uidsMtx.Lock()
	lm, exists := lmc.launching[uid]

	if !exists {
		lmc.launching[uid] = &launchingMon{
			waiting: []chan bool{},
			mtx:     sync.Mutex{},
		}

		lmc.uidsMtx.Unlock()
		return false
	}
	lmc.uidsMtx.Unlock()

	// monitor is launching, add chan
	lm.mtx.Lock()

	myPos := len(lm.waiting)
	ch := make(chan bool)
	lm.waiting = append(lm.waiting, ch)

	lm.mtx.Unlock()

	// check if mon launched
	launchSuccess := <-ch
	if launchSuccess {
		return true
	}

	// return false, so a different bot attempts to launch it
	if myPos == 0 {
		lm.mtx.Lock()
		lm.waiting = []chan bool{}
		lm.mtx.Unlock()
		return false
	}

	// wait recursively
	return lmc.Wait(uid)
}

// successive calls to Finished after success will pass silently
func (lmc *LaunchingMonitorsCache) Finished(uid string, isSuccess bool) {
	lmc.uidsMtx.Lock()
	defer lmc.uidsMtx.Unlock()

	lm, exists := lmc.launching[uid]
	if !exists {
		return
	}

	lm.mtx.Lock()
	defer lm.mtx.Unlock()

	for _, ch := range lm.waiting {
		ch <- isSuccess
	}

	if isSuccess {
		delete(lmc.launching, uid)
	}
}
