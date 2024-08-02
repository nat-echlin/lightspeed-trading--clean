package db

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type User struct {
	UserID           string
	Plan             string
	RenewalTS        time.Time
	AlertWebhook     string
	RemainingCpytBal decimal.Decimal
}

func (u User) String() string {
	return fmt.Sprintf(
		"User{UserID: %s, Plan: %s, RenewalTS: %v, AlertWebhook: %s, RemainingCpytBalance: %s}",
		u.UserID, u.Plan, u.RenewalTS, u.AlertWebhook, u.RemainingCpytBal,
	)
}
