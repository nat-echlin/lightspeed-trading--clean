package cls

import (
	"net/http"
	"time"
)

// earliestUse is a timestamp of when it can next be used
type ClientProxy struct {
	Client      *http.Client
	Proxy       string
	EarliestUse time.Time
	RateLimit   time.Duration
}

// sleep until the given time.time
func (c *ClientProxy) SleepUntilReady() {
	now := time.Now()
	duration := c.EarliestUse.Sub(now)

	if duration > 0 {
		time.Sleep(duration)
	}
}

// resets the earliestUse to when it will next be available for use
func (c *ClientProxy) ResetUseTime() {
	c.EarliestUse = time.Now().Add(c.RateLimit)
}
