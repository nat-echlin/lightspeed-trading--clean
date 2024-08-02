package cls

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type BotLogs struct {
	// stores logs from the last 48 hours
	Logs []BotLog `json:"botLogs"`
	Mtx  sync.Mutex
}

type BotLog struct {
	Ts  time.Time
	Msg string
}

func (bl *BotLogs) Logf(format string, args ...interface{}) {
	bl.Mtx.Lock()
	defer bl.Mtx.Unlock()

	msg := fmt.Sprintf(format, args...)
	tNow := time.Now().In(time.UTC)
	newLog := BotLog{Ts: tNow, Msg: msg}

	// append to the back, so a lower index means a longer ago log, and
	// a larger index means a more recent log
	bl.Logs = append(bl.Logs, newLog)

	// remove logs older than 48 hours. We know this will work because
	// the logs are already in chronological order, so once we get to one
	// that's not reached the 48 hour cutoff yet, we know that's the oldest
	// allowed logs - and anything that we've already iterated past is
	// a bad log that we should delete
	cutoff := time.Now().In(time.UTC).Add(-48 * time.Hour)
	numLogs := len(bl.Logs)

	for i := 0; i <= numLogs-1; i++ {
		if bl.Logs[i].Ts.After(cutoff) {
			bl.Logs = bl.Logs[i:]
			break
		}
	}
}

// return the logs as json serialized string, as a []BotLog, but
// the Ts field (time.Time) is represented as a UTC timestamp
func (bl *BotLogs) Get() (string, error) {
	bl.Mtx.Lock()
	defer bl.Mtx.Unlock()

	// create an anonymous struct type to hold the transformed logs
	transformedLogs := make([]struct {
		Ts  int64  `json:"ts"`
		Msg string `json:"msg"`
	}, len(bl.Logs))

	// convert the time.Time to a Unix timestamp
	for i, log := range bl.Logs {
		transformedLogs[i].Ts = log.Ts.Unix()
		transformedLogs[i].Msg = log.Msg
	}

	// serialize the transformed logs to JSON
	jsonLogs, err := json.Marshal(transformedLogs)
	if err != nil {
		return "", err
	}

	return string(jsonLogs), nil
}
