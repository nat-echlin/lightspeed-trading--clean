package common

import (
	"errors"
	"fmt"
	"log"

	cls "github.com/lightspeed-trading/app/classes"
	dwh "github.com/nat-echlin/dwhooks"
)

var LIGHTSPEED_BOT_PROFILE_PICTURE_URL string
var STAFF_DISC_WH_URL string

// Send a notificatino to a user that their renewal is due soon.
// Will log its own success.
func SendRenewalNoti(user cls.NotificationUser) error {
	// check user.AlertWebhook is not emtpy
	if user.AlertWebhook == "" {
		return errors.New("cannot send notification to an empty user.AlertWebhook")
	}

	// create embed
	emb := dwh.NewEmbed()
	emb.SetTitle("Your renewal is approaching!")

	dayORdays := "days"
	if user.DaysAway == 1 {
		dayORdays = "day"
	}
	desc := fmt.Sprintf(
		"We hope you've enjoyed using LightSpeed so far, and there's only %d %s left on your membership.\n\nTo continue for another month, please renew your plan for $%.0f at the link below.\n\nhttps://lightspeedapp.xyz/renew?plan=%s",
		user.DaysAway, dayORdays, user.ExpectedRenewal, user.Plan,
	)
	emb.SetDescription(desc)
	emb.SetColour(255) // blue

	// create message, add pfp, username
	msg := dwh.NewMessage("")
	msg.SetUsername("LightSpeed Renewals")
	msg.SetAvatarURL(LIGHTSPEED_BOT_PROFILE_PICTURE_URL)
	msg.AddEmbed(emb)

	status, err := dwh.NewWebhook(user.AlertWebhook).Send(msg)
	if err != nil || (status < 200 || status > 299) {
		return fmt.Errorf("status: %d, err: %v", status, err)
	}

	log.Printf(
		"%s : successfully sent renewal notification (%d %s)",
		user.UserID, user.DaysAway, dayORdays,
	)

	return nil
}

// send an alert to the staff alerts webhook
func SendStaffAlert(
	desc string,
) error {
	msg := dwh.NewMessage(desc)

	wh := dwh.NewWebhook(STAFF_DISC_WH_URL)
	status, err := wh.Send(msg)

	if err != nil {
		return fmt.Errorf("failed to send to webhook, %v", err)
	}
	expectedStatus := 204
	if status != expectedStatus {
		return fmt.Errorf("bad status; expected: %d, got: %d", expectedStatus, status)
	}
	return nil
}

// Send a notificatino to a user that their renewal is due soon.
// Will log its own success
func sendRenewalNoti(user cls.NotificationUser) error {
	// create embed
	emb := dwh.NewEmbed()
	emb.SetTitle("Your renewal is approaching!")

	dayORdays := "days"
	if user.DaysAway == 1 {
		dayORdays = "day"
	}
	desc := fmt.Sprintf(
		"We hope you've enjoyed using LightSpeed so far, and there's only %d %s left on your membership.\n\nTo continue for another month, please renew your plan for $%.0f at the link below.\n\nhttps://lightspeedapp.xyz/renew?plan=%s",
		user.DaysAway, dayORdays, user.ExpectedRenewal, user.Plan,
	) // TODO #27 add a link to the dashboard in the sendRenewalNoti notification message
	emb.SetDescription(desc)
	emb.SetColour(255) // blue

	// create message, add pfp, username
	msg := dwh.NewMessage("")
	msg.SetUsername("LightSpeed Renewals")
	msg.SetAvatarURL(LIGHTSPEED_BOT_PROFILE_PICTURE_URL)
	msg.AddEmbed(emb)

	status, err := dwh.NewWebhook(user.AlertWebhook).Send(msg)
	if err != nil || (status < 200 || status > 299) {
		return fmt.Errorf("status: %d, err: %v", status, err)
	}

	log.Printf(
		"%s : successfully sent renewal notification (%d %s)",
		user.UserID, user.DaysAway, dayORdays,
	)

	return nil
}

// log to stdout, and send as a staff alert. internally launched as a goroutine
func LogAndSendAlertF(str string, v ...any) {
	go func() {
		msg := fmt.Sprintf("STAFF-ALERT : "+str, v...)
		log.Print(msg)

		SendStaffAlert(msg)
	}()
}

// For discord bot alerts use the following colours
const (
	DiscordAlertColourOpenPosition   = 65280    // #00FF00 (green)
	DiscordAlertColourClosePosition  = 16711680 // #FF0000 (red)
	DiscordAlertColourAddToPosition  = 16776960 // #FFFF00 (yellow)
	DiscordAlertColourReducePosition = 16753920 // #FFA500 (orange)
	DiscordAlertColourBotError       = 8388608  // #800000 (dark red)
	DiscordAlertColourBotAlert       = 255      // #0000FF (blue)
)
