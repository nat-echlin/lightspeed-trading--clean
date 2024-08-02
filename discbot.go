package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lightspeed-trading/app/common"
)

var LSBOT string = "LSBOT_DISC"

// long running function, start with a go routine
func launchDiscordBot(
	botToken string,
	pool *pgxpool.Pool,
	fnameChan <-chan string,
	tradesDiscordChannelID string,
) {
	defer func() {
		if err := recover(); err != nil {
			alert := fmt.Sprintf("discord bot crashed, restarting\n\n%v", err)
			common.SendStaffAlert(alert)

			launchDiscordBot(botToken, pool, fnameChan, tradesDiscordChannelID)
		}
	}()

	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		log.Fatalf("%s : failed to create session, %v", LSBOT, err)
	}

	// add handlers
	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		onMessageCreated(s, m, pool)
	})

	// launch bot
	session.Identify.Intents = discordgo.IntentsAll

	err = session.Open()
	if err != nil {
		log.Fatalf("%s : failed to open session, %v", LSBOT, err)
	}
	defer session.Close()

	go listenForTradesFiles(session, fnameChan, tradesDiscordChannelID)

	log.Printf("%s : %s is now online", LSBOT, session.State.User.Username)
	for {
		time.Sleep(999999999999)
	}
}

func listenForTradesFiles(
	s *discordgo.Session,
	fnameChan <-chan string,
	tradesDiscordChannelID string,
) {
	log.Printf("%s : listening for trade files to send…", LSBOT)

	for {
		err := getTradesFiles(fnameChan, s, tradesDiscordChannelID)
		if err != nil {
			common.LogAndSendAlertF(err.Error())
			continue
		}
		log.Printf("%s : success sending trades to trades discord channel", LSBOT)
	}
}

func getTradesFiles(fnameChan <-chan string, s *discordgo.Session, tradesDiscordChannelID string) error {
	filename := <-fnameChan

	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf(
			"%s : failed to open trades file %s, %v",
			LSBOT, filename, err,
		)

	}

	_, err = s.ChannelFileSend(tradesDiscordChannelID, filename, file)
	if err != nil {
		file.Close()
		return fmt.Errorf(
			"%s : failed to send trades file to discord, %v",
			LSBOT, err,
		)
	}

	file.Close()
	return nil
}

func onMessageCreated(
	s *discordgo.Session,
	m *discordgo.MessageCreate,
	pool *pgxpool.Pool,
) {
	if m.Author.ID == s.State.User.ID {
		return // skip if message is sent by the bot
	}

	if strings.HasPrefix(m.Content, "!transfer") && isAdmin(m.Member, m.GuildID) {
		commandTransfer(s, m, pool)

	} else if strings.HasPrefix(m.Content, "!setUserPlan") && isAdmin(m.Member, m.GuildID) {
		commandSetUserPlan(s, m, pool)

	} else if strings.HasPrefix(m.Content, "!setNumInstances") && isAdmin(m.Member, m.GuildID) {
		commandSetNumInstances(s, m, pool)
	} else if strings.HasPrefix(m.Content, "!getAuthID") && isAdmin(m.Member, m.GuildID) {
		commandGetAuthID(s, m, pool)
	}
}

// !getAuthID
func commandGetAuthID(
	s *discordgo.Session,
	m *discordgo.MessageCreate,
	pool *pgxpool.Pool,
) {
	log.Printf("%s : received !getAuthID request from %s", LSBOT, m.Author.Username)

	sendErr := func() {
		s.ChannelMessageSend(
			m.ChannelID,
			"Structure: !getAuthID *auth0Email*, eg !getAuthID test1@test.com",
		)
	}

	// parse request
	parts := strings.Split(m.Content, " ")
	if len(parts) != 2 {
		sendErr()
		return
	}
	email := parts[1]

	// get auth0 user id
	userID, userShowable, err := getAuth0UserID(email)
	if err != nil {
		log.Printf("%s : failed to get userID for email %s, %v", LSBOT, email, err)
		if userShowable {
			s.ChannelMessageSend(m.ChannelID, err.Error())
		} else {
			s.ChannelMessageSend(
				m.ChannelID,
				"Internal server error (getting auth0 userID)",
			)
		}
		return
	}

	log.Printf("%s : found auth0 userID (%s) for email (%s)", LSBOT, userID, email)
	s.ChannelMessageSend(m.ChannelID, userID)
}

// !transfer
func commandTransfer(
	s *discordgo.Session,
	m *discordgo.MessageCreate,
	pool *pgxpool.Pool,
) {
	log.Printf("%s : received transfer request from %s", LSBOT, m.Author.Username)

	sendErr := func() {
		s.ChannelMessageSend(
			m.ChannelID,
			"Structure: !transfer *auth0Email* *@discordUser*, eg !transfer nat@email.com @nat",
		)
	}

	// parse request
	parts := strings.Split(m.Content, " ")
	if len(parts) != 3 {
		sendErr()
		return
	}
	email := parts[1]
	rawDiscordID := parts[2]

	discordID := rawDiscordID[2 : len(rawDiscordID)-1]

	log.Printf("%s : transferring email: %s, discordID: %s", LSBOT, email, discordID)

	// get auth0 user id
	userID, userShowable, err := getAuth0UserID(email)
	if err != nil {
		log.Printf("%s : failed to get userID for email %s, %v", LSBOT, email, err)
		if !userShowable {
			s.ChannelMessageSend(
				m.ChannelID,
				"Internal server error (getting auth0 userID)",
			)
		} else {
			s.ChannelMessageSend(m.ChannelID, err.Error())
		}
		return
	}

	// get whopProduct off whop
	whopProduct, daysLeft, userShowable, err := getWhopPlanForDiscordID(discordID)
	if err != nil {
		log.Printf(
			"%s : failed to verify plan from whop for discordID %s, %v",
			LSBOT, discordID, err,
		)
		if !userShowable {
			s.ChannelMessageSend(
				m.ChannelID,
				"Internal server error (getting whop plan for user)",
			)
		} else {
			s.ChannelMessageSend(m.ChannelID, err.Error())
		}
		return
	}

	// convert to the new plan names
	modernPlan, exists := convertWhopProductToModernPlan(whopProduct)
	if !exists {
		msg := fmt.Sprintf(
			"%s : %s has an unknown whop access_pass / product: %s",
			LSBOT, discordID, whopProduct,
		)
		log.Print(msg)

		s.ChannelMessageSend(m.ChannelID, msg)
		return
	}

	// run new plan
	daysLeftp := int(daysLeft)
	status, err := SetUserPlan(
		SetUserPlanReq{
			TargetUserID:      userID,
			PlanName:          modernPlan,
			OverwriteExisting: true,
			DaysUntilRenewal:  &daysLeftp,
		},
		pool,
	)

	// send result to user
	msg := fmt.Sprint(status)
	if status != 200 {
		msg += " " + err.Error()
	}
	s.ChannelMessageSend(m.ChannelID, msg)

	log.Printf("%s : sent %s", LSBOT, msg)
}

// !setUserPlan auth0Email
func commandSetUserPlan(
	s *discordgo.Session,
	m *discordgo.MessageCreate,
	pool *pgxpool.Pool,
) {
	sendErr := func() {
		s.ChannelMessageSend(
			m.ChannelID,
			"Structure: !setUserPlan *auth0Email planName daysTillRenewal*, eg !setUserPlan nat@email.com copytradeTier1 30",
		)
	}

	// parse request
	params := strings.Split(m.Message.Content, " ")
	if len(params) != 4 {
		sendErr()
		return
	}

	auth0Email := params[1]
	planName := params[2]
	daysTillRenewalStr := params[3]

	log.Printf(
		"%s : received !setUserPlan request, for: %s %s %s",
		LSBOT, auth0Email, planName, daysTillRenewalStr,
	)

	// get userID from auth0
	userID, userShowable, err := getAuth0UserID(auth0Email)
	if err != nil {
		log.Printf(
			"%s : failed to get userID for email %s, %v",
			LSBOT, auth0Email, err,
		)

		if !userShowable {
			s.ChannelMessageSend(
				m.ChannelID,
				"Internal server error (getting auth0 userID)",
			)
		} else {
			s.ChannelMessageSend(m.ChannelID, err.Error())
		}
		return
	}

	var daysLeftP *int
	if daysTillRenewalStr == "x" {
		daysLeftP = nil
	} else {
		daysTillRenewal, err := strconv.ParseInt(daysTillRenewalStr, 10, 64)
		if err != nil {
			log.Printf(
				"%s : failed to parse daysTillRenewal for !setUserPlan, %v",
				LSBOT, err,
			)
			sendErr()
			return
		}
		daysLeftInt := int(daysTillRenewal)
		daysLeftP = &daysLeftInt
	}

	// execute request
	status, err := SetUserPlan(
		SetUserPlanReq{
			TargetUserID:      userID,
			PlanName:          planName,
			OverwriteExisting: true,
			DaysUntilRenewal:  daysLeftP,
		},
		pool,
	)

	// send message back to user
	msg := fmt.Sprint(status)
	if status != 200 {
		msg += " " + err.Error()
	}
	s.ChannelMessageSend(m.ChannelID, msg)

	log.Printf("%s : sent %s", LSBOT, msg)
}

func commandSetNumInstances(
	s *discordgo.Session,
	m *discordgo.MessageCreate,
	pool *pgxpool.Pool,
) {
	sendErr := func() {
		s.ChannelMessageSend(
			m.ChannelID,
			"Structure: !setNumInstances *auth0Email numInstances*, eg !setNumInstances nat@email.com 3",
		)
	}

	// parse request
	params := strings.Split(m.Message.Content, " ")
	if len(params) != 3 {
		sendErr()
		return
	}

	auth0Email := params[1]
	numInstancesInt64, err := strconv.ParseInt(params[2], 10, 32)
	if err != nil {
		log.Printf("%s : failed to parse params for !setUserPlan, %v", LSBOT, err)
		sendErr()
		return
	}
	numInstances := int(numInstancesInt64)

	log.Printf(
		"%s : received !setNumInstances request, for: %s %d",
		LSBOT, auth0Email, numInstances,
	)

	// get userID from auth0
	userID, userShowable, err := getAuth0UserID(auth0Email)
	if err != nil {
		log.Printf("%s : failed to get userID for email %s, %v", LSBOT, auth0Email, err)
		if !userShowable {
			s.ChannelMessageSend(
				m.ChannelID,
				"Internal server error (getting auth0 userID)",
			)
		} else {
			s.ChannelMessageSend(m.ChannelID, err.Error())
		}
		return
	}

	// execute, return msg to user
	status, err := SetNumInstances(userID, numInstances, pool)

	msg := fmt.Sprint(status)
	if status != 200 {
		msg += " " + err.Error()
	}
	s.ChannelMessageSend(m.ChannelID, msg)
	log.Printf("%s : sent %s", LSBOT, msg)
}

type Auth0GetUserByEmailResponse []struct { // there are more fields, but omitted for brevity
	UserID string `json:"user_id"`
}

// returns their userID from their email.
// bool is whether the error can be shown to user
func getAuth0UserID(email string) (string, bool, error) {
	// make reuqest
	url := fmt.Sprintf(
		"https://dev-4sdvlhh72l5jt8yf.us.auth0.com/api/v2/users-by-email?email=%s",
		email,
	)

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", false, fmt.Errorf("failed to create request, %v", err)
	}

	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+AUTH0_API_TOKEN)

	res, err := client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("failed to make request, %v", err)
	}

	// parse userID
	var respData Auth0GetUserByEmailResponse
	err = json.NewDecoder(res.Body).Decode(&respData)
	if err != nil {
		return "", false, fmt.Errorf("failed to decode json, %v", err)
	}

	numAccounts := len(respData)
	if numAccounts != 1 {
		return "", true, fmt.Errorf(
			"user has %d accounts with the email %s",
			numAccounts, email,
		)
	}

	userID := respData[0].UserID

	return userID, false, nil
}

// check if a discordID is valid, and if it is, get its plan.
// the returned integer is how many days are left on the plan, rounded up
// bool is whether it can be shown directly to a user
func getWhopPlanForDiscordID(discordID string) (string, int64, bool, error) {
	client := http.Client{}

	// get data of whop, parse it
	whopData, err := getWhopData(client, discordID)
	if err != nil {
		return "", 0, false, err
	}

	numPlans := len(whopData.Data)
	if numPlans != 1 {
		return "", 0, true, fmt.Errorf("user has %d plans on whop", numPlans)
	}

	whopPlan := whopData.Data[0].Product
	renewalTS := whopData.Data[0].RenewalEndTS

	renewalTime := time.Unix(renewalTS, 0)
	currentTime := time.Now().UTC()
	duration := renewalTime.Sub(currentTime)
	days := math.Ceil(duration.Hours() / 24)

	return whopPlan, int64(days), false, nil
}

// the response that whop sends us from a key request
type whopResponse struct {
	Data []struct {
		Status       string `json:"status"`
		Valid        bool   `json:"valid"`
		LicenseKey   string `json:"license_key"`
		Product      string `json:"product"`
		RenewalEndTS int64  `json:"renewal_period_end"`
		Discord      struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"discord"`
	} `json:"data"`
}

// for one individual user, get the whop data
func getWhopData(client http.Client, discordID string) (whopResponse, error) {
	// create the request
	req, err := http.NewRequest(
		"GET",
		fmt.Sprintf(
			"https://api.whop.com/api/v2/memberships?discord_id=%s&valid=true",
			discordID,
		),
		nil,
	)
	if err != nil {
		return *new(whopResponse), fmt.Errorf("failed to create request, %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+WHOP_API_TOKEN)

	resp, err := client.Do(req)
	if err != nil {
		return *new(whopResponse), fmt.Errorf("failed to get a response, %v", err)
	}

	// parse response
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return *new(whopResponse), fmt.Errorf("failed to read body, %v", err)
	}

	var respJSON whopResponse
	json.Unmarshal(body, &respJSON)

	return respJSON, nil
}

// check if a user is an admin or not
func isAdmin(member *discordgo.Member, guildID string) bool {
	targetRoleID := PROD_ADMIN_ROLE_ID
	if guildID == DEV_GUILD_ID {
		targetRoleID = DEV_ADMIN_ROLE_ID
	}

	for _, roleID := range member.Roles {
		if roleID == targetRoleID {
			return true
		}
	}
	return false
}

// returned bool is whether it exists or not
func convertWhopProductToModernPlan(whopProduct string) (string, bool) {
	translator := map[string]string{
		"prod_VP6Kag2HJOGEY": "copytradeTier1",
		"prod_ZDjf2WfGSuhfi": "copytradeTier2",
		"prod_o3B3mThd9fXsI": "copytradeTier3",
	}
	modernProduct, exists := translator[whopProduct]
	return modernProduct, exists
}
