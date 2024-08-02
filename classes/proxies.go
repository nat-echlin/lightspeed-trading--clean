package cls

import (
	"fmt"
	"sync"
)

type Proxies struct {
	Available []string
	Mu        sync.Mutex
}

func (proxies *Proxies) Length() int {
	proxies.Mu.Lock()
	defer proxies.Mu.Unlock()

	return len(proxies.Available)
}

func (proxies *Proxies) NextGroup(wantNum int) ([]string, error) {
	proxies.Mu.Lock()
	defer proxies.Mu.Unlock()

	haveNum := len(proxies.Available)
	if haveNum < wantNum {
		return nil, fmt.Errorf("not enough proxies available (want %d, have %d)", wantNum, haveNum)
	}

	group := proxies.Available[:wantNum]
	proxies.Available = proxies.Available[wantNum:]

	return group, nil
}

func (proxies *Proxies) ReturnGroup(group []string) {
	proxies.Mu.Lock()
	defer proxies.Mu.Unlock()

	proxies.Available = append(proxies.Available, group...)
}
