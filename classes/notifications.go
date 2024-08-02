package cls

import "time"

// attributes for sending notifications
type NotifAttr struct {
	Name  string
	Value string
}

type NotificationUser struct {
	UserID          string
	Plan            string
	RenewalTS       time.Time
	AlertWebhook    string
	DaysAway        int // 0 or negative if already expired
	ExpectedRenewal float64
}
