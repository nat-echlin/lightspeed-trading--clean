package cls

import (
	"errors"
	"sync"
)

var ErrCantRenewNonexistantUser = errors.New("can't renew a user who hasn't already paid their initial")

type PaymentsHdlr struct {
	Payments map[string]Payment // string key get from getPaymentsKey
	Mu       sync.Mutex
}

type Payment struct {
	UserID         string
	TxID           string
	IsSuccess      bool
	ReceivedQtyEth float64
	ErrStr         string
}

func GetPaymentsKey(txID string) string {
	return txID
}
