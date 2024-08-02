package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	"github.com/lightspeed-trading/app/db"
	dwh "github.com/nat-echlin/dwhooks"
)

// send a notificatin with error description (desc) to the users preferred
// platform if whUrl is an empty string (""), _sendNotif will attempt to open the bots
// config file from the database. If that fails it will send a staff alert that it
// failed to get the users config
func _sendNotif(
	title string,
	desc string,
	colour int,
	attrs []cls.NotifAttr,
	whUrl string,
	userID string,
	botID int,
) {
	// get notifaction destination
	if whUrl == "" {
		mySettings, err := getConfig(userID, botID)
		if err != nil {
			go common.LogAndSendAlertF(
				"%s : failed to read users config when sending notification, %v\nmsg: %s",
				userID, err, desc,
			)
			return
		}
		whUrl = mySettings.NotifyDest
	}

	// send to notif target
	if strings.Contains(whUrl, "discord") {
		emb := dwh.NewEmbed()
		emb.SetTitle(title)
		emb.SetDescription(desc)
		emb.SetColour(colour)
		emb.SetTimestamp(time.Now().Unix())

		for _, a := range attrs {
			emb.AddField(a.Name, a.Value, true)
		}

		// add pfp, username
		msg := dwh.NewMessage("")
		msg.SetUsername("LightSpeed Helper")
		msg.SetAvatarURL(LIGHTSPEED_BOT_PROFILE_PICTURE_URL)
		msg.AddEmbed(emb)

		// send message to discord
		sta, err := dwh.NewWebhook(whUrl).Send(msg)
		if err != nil || (sta < 200 || sta > 299) {
			log.Printf("failed to send error notif, sta: %d, err: %v", sta, err)
		}

	} else {
		panic("user has a non supported platform set as notification destination")
	}
}

// send a notification to the user that a position has been opened
func (bot *Bot) sendPositionOpenedNotif(pos BotPosition, whUrl string, botName string) {
	_sendNotif(
		"Position Opened",
		cls.GetPingDescription(pos.Side, pos.Qty, pos.Symbol),
		common.DiscordAlertColourOpenPosition,
		append(
			bot.getBasicNotifAttrs(botName),
			cls.NotifAttr{Name: "Leverage", Value: pos.Leverage.String()},
			cls.NotifAttr{Name: "Exchange", Value: pos.Exchange},
		),
		whUrl,
		bot.UserID,
		bot.BotID,
	)

	log.Printf(
		"%s : sent position opened notification for %s %s",
		bot.getHash(), pos.Side, pos.Symbol,
	)
}

// send a notification to the user that a position has been closed
func (bot *Bot) sendPosClosedNotif(
	pos BotPosition,
	pnl *ClosedPNL,
	whUrl string,
	botName string,
) {
	// only add pnl if its not nil
	attrs := bot.getBasicNotifAttrs(botName)
	var desc string

	if pnl != nil {
		attrs = addPnlToAttrs(attrs, pnl)
		desc = cls.GetPingDescription(pos.Side, pnl.Qty, pos.Symbol)
	} else {
		desc = cls.GetPingDescription(pos.Side, pos.Qty, pos.Symbol)
	}

	_sendNotif(
		"Position Closed",
		desc,
		common.DiscordAlertColourClosePosition,
		attrs,
		whUrl,
		bot.UserID,
		bot.BotID,
	)

	log.Printf(
		"%s : sent position closed notification for %s %s",
		bot.getHash(), pos.Side, pos.Symbol,
	)
}

// get a default list of notif attrs (Trader, Bot Name)
func (bot *Bot) getBasicNotifAttrs(botName string) []cls.NotifAttr {
	attrs := []cls.NotifAttr{
		{Name: "Trader", Value: bot.Trader},
	}
	if botName != "" {
		return append(attrs, cls.NotifAttr{Name: "Bot", Value: botName})
	} else {
		return append(attrs, cls.NotifAttr{Name: "Bot", Value: fmt.Sprint(bot.BotID)})
	}
}

// given a pnl, format it properly for a discord embed
func addPnlToAttrs(attrs []cls.NotifAttr, pnl *ClosedPNL) []cls.NotifAttr {
	if pnl != nil {
		pnlvalue := truncate(pnl.Pnl, 2)

		// check if the pnl is negative, a loss :(
		var pnlStr string
		if pnl.Pnl < 0 {
			absVal := pnlvalue[1:]

			// below, the \ is needed to escape discord formatting "- ..." as a bullet
			// point and we need two \ ("\\") to escape go treating it as the start of
			// a go string escape sequence.
			pnlStr = "\\- $" + absVal
		} else {
			pnlStr = "+ $" + pnlvalue
		}

		// add attributes
		return append(attrs,
			cls.NotifAttr{Name: "PnL", Value: pnlStr},
			cls.NotifAttr{Name: "Average Close Price", Value: fmt.Sprintln(pnl.AvgExitPrice)},
		)
	} else {
		return attrs
	}
}

// send a noti that the bot has had a big error. Automatically adds instruction for
// the user to check their positions for a potential order.
func (bot *Bot) sendErrorNotif(desc string, settings db.BotConfig) {
	// add instructions to check their positions
	desc = fmt.Sprintf("%s\nPlease check your positions for potential issues.", desc)

	_sendNotif(
		"Error",
		desc,
		16711680,
		bot.getBasicNotifAttrs(settings.BotName),
		settings.NotifyDest,
		bot.UserID,
		bot.BotID,
	)
}

// send a non-error alert eg: not opening btcusdt because you have blacklisted it
func (bot *Bot) sendAlertNotif(desc string, settings db.BotConfig) {
	_sendNotif(
		"Alert",
		desc,
		255,
		bot.getBasicNotifAttrs(settings.BotName),
		settings.NotifyDest,
		bot.UserID,
		bot.BotID,
	)
}

// send a notification after a position increase
// pos is the bots new position in the symbol
func (bot *Bot) sendPositionAddedToPing(
	pos BotPosition,
	whUrl string,
	botName string,
) {
	_sendNotif(
		"Position Added To",
		cls.GetPingDescription(pos.Side, pos.Qty, pos.Symbol),
		common.DiscordAlertColourAddToPosition,
		append(
			bot.getBasicNotifAttrs(botName),
			cls.NotifAttr{Name: "Leverage", Value: pos.Leverage.String()},
			cls.NotifAttr{Name: "Exchange", Value: pos.Exchange},
		),
		whUrl, bot.UserID, bot.BotID,
	)

	log.Printf(
		"%s : sent position added to notification for %s %s",
		bot.getHash(), pos.Side, pos.Symbol,
	)
}

// send a notification after a position is reduced
func (bot *Bot) sendPositionReducedPing(
	pos BotPosition,
	pnl *ClosedPNL,
	whUrl string,
	botName string,
) {
	// only add pnl if its not nil
	attrs := bot.getBasicNotifAttrs(botName)
	var desc string

	if pnl != nil {
		attrs = addPnlToAttrs(attrs, pnl)
		desc = cls.GetPingDescription(pos.Side, pnl.Qty, pos.Symbol)
	} else {
		desc = cls.GetPingDescription(pos.Side, pos.Qty, pos.Symbol)
	}

	_sendNotif(
		"Position Partially Sold", desc,
		common.DiscordAlertColourClosePosition, attrs, whUrl,
		bot.UserID, bot.BotID,
	)

	log.Printf(
		"%s : sent position partially sold notification for %s %s",
		bot.getHash(), pos.Side, pos.Symbol,
	)
}
