package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lib/pq"
	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/db"
	"github.com/shopspring/decimal"
)

type ListBotsResponse struct {
	Err  string         `json:"error"`
	Bots []db.BotConfig `json:"bots,omitempty"`
}

func concealApiKey(key string) string {
	str := fmt.Sprintf("%-8s", key)
	return str[:3] + strings.Repeat("*", len(str)-3)
}

// returns a list of all bots (active or not) that are currently running
func OnListBots(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, encryptionKey string) {
	resp := ListBotsResponse{
		Err:  "",
		Bots: []db.BotConfig{},
	}

	// get user id
	userID, err := extractUserID(r)
	if err != nil {
		log.Printf("failed to authorise, %v", err)

		resp.Err = "Authorization error"
		sendStructToUser(resp, w, 401)
		return
	}
	log.Printf("%s : received onListBots request", userID)

	// request from psql all rows with that userid (ie all the bots)
	bots, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		log.Printf("%s : failed to read from db, %v", userID, err)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	// remove UserID field, and show a concealed api key to user
	for i, bot := range bots {
		bots[i].UserID = ""

		if len(bot.APISecretRaw) > 0 {
			bots[i].APISecretRaw = []byte("******")
		}

		if len(bot.APIKeyRaw) > 0 {
			apiKey, err := db.Decrypt(bots[i].APIKeyRaw, encryptionKey)
			if err != nil {
				// failed to decrypt api keys
				log.Printf("%s : failed to decrypt api keys, %v", userID, err)
				resp.Err = "Internal server error"
				sendStructToUser(resp, w, 500)
				return
			}
			bots[i].APIKeyRaw = []byte(concealApiKey(apiKey))
		}
	}
	// send to user
	resp.Bots = bots
	sendStructToUser(resp, w, 200)

	log.Printf("%s : served %d bots to user", userID, len(bots))
}

type UpdateSettingsRequest struct {
	BotID        int                    `json:"botID"`
	OldTraderUID string                 `json:"oldTraderUID"`
	Settings     map[string]interface{} `json:"settings"` // only the CHANGED settings go here
}

type UpdateSettingsResponse struct {
	UpdateFailed []string `json:"updateFailed"` // json key that the server couldnt update
	Err          string   `json:"err"`
}

// used to update settings of a user
// request body must be a UpdateSettingsRequest json
func onUpdateSettings(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	encryptionKey string,
	preventRestart bool,
	lmc *cls.LaunchingMonitorsCache,
) {
	resp := UpdateSettingsResponse{
		UpdateFailed: []string{},
		Err:          "",
	}

	// get user id
	userID, err := extractUserID(r)
	if err != nil {
		log.Printf("failed to authorise, %v", err)

		resp.Err = "Authorization error"
		sendStructToUser(resp, w, 401)
		return
	}

	// decode the request
	var req UpdateSettingsRequest
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("bad json request, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, http.StatusBadRequest)
		return
	}
	botHash := fmt.Sprintf("%s_%d", userID, req.BotID)
	log.Printf("%s : received onUpdateSettings request, %v", botHash, req)

	// get the old config
	oldConfig, err := db.ReadRecordCPYT(pool, userID, req.BotID)
	if err != nil {
		log.Printf("%s : failed to read old config from db, %v", botHash, err)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	// get update query, args etc
	args, query, updateFailed, doRestartBot := getUpdateSettingsSQL(userID, req, encryptionKey, oldConfig)

	// execute sql query
	var status int

	if len(updateFailed) == 0 {
		_, err = pool.Exec(context.Background(), query, args...)
		if err != nil {
			log.Printf("%s : unable to execute the query: %v", botHash, err)

			resp.Err = "Internal server error"
			sendStructToUser(resp, w, 500)
			return
		}

		if doRestartBot && !preventRestart && oldConfig.BotStatus == db.BotActive {
			log.Printf("%s : restarting bot", botHash)
			restartBotAfterSettingsChange(userID, req.BotID,
				oldConfig.BinanceID, pool, encryptionKey, lmc,
			)
		}

		log.Printf("%s : executed update for all keys", botHash)
		status = 200

	} else {
		resp.UpdateFailed = updateFailed

		log.Printf("%s : not executing update, %v", botHash, updateFailed)
		status = 400
	}

	sendStructToUser(resp, w, status)
}

// update a users settings. Returns: 1. args for the sql query, 2. the sql query
// 3. slice of JSON keys that it couldnt do the update for, 4. bool whether it
// needs to restart the bot or not.
func getUpdateSettingsSQL(
	userID string,
	req UpdateSettingsRequest,
	encryptionKey string,
	oldConfig db.BotConfig,
) ([]interface{}, string, []string, bool) {
	var queryBuilder strings.Builder
	queryBuilder.WriteString("UPDATE copytrading SET ")

	args := []interface{}{userID, req.BotID}
	argCount := 3
	doRestartBot := false
	updateFailed := []string{}

	for jsonKey, value := range req.Settings {

		// get the sql key from the json key
		key, exists, requiresRestart, expectedType := db.GetSqlKey(jsonKey)
		if !exists {
			log.Printf("%s : bad key given %s", userID, jsonKey)
			updateFailed = append(updateFailed, jsonKey)
			continue
		}

		// make sure the type given by user is correct
		gotType := reflect.TypeOf(value)
		if expectedType.Kind() == reflect.Float64 {
			val, isInt := value.(int)
			if isInt {
				nv := float64(val)
				value = nv
			}
		}
		if expectedType.Kind() == reflect.Int {
			val, isFloat := value.(float64)
			if isFloat {
				nv := int(val)
				value = nv
			}
		}
		if reflect.TypeOf(value) != expectedType {
			log.Printf("%s : wrong type for %s: got %s, expected %s", userID, jsonKey, gotType, expectedType)
			updateFailed = append(updateFailed, jsonKey)
			continue
		}

		// check mutually exclusives
		if (jsonKey == "whitelistCoins" && len(oldConfig.BlacklistCoins) > 0) ||
			(jsonKey == "blacklistCoins" && len(oldConfig.WhitelistCoins) > 0) {
			log.Printf(
				"%s : cant update %s, need blacklist and whitelist to be mutually exclusive. old blacklist: %v, old whitelist: %v",
				userID,
				jsonKey,
				oldConfig.BlacklistCoins,
				oldConfig.WhitelistCoins,
			)
			updateFailed = append(updateFailed, jsonKey)
			continue
		}

		// see if it will need to be restarted
		if requiresRestart {
			doRestartBot = true
		}

		switch valType := value.(type) {
		case float64, int, string, bool:
			queryBuilder.WriteString(fmt.Sprintf("%s = $%d, ", key, argCount))

			// check if they need encryption
			if jsonKey == "apiKeyRaw" || jsonKey == "apiSecretRaw" {
				newApiInfo, isStr := value.(string)
				if !isStr {
					log.Printf("%s : got bad value %v for key %s", userID, value, key)
					updateFailed = append(updateFailed, jsonKey)
					continue
				}

				encryptedData, err := db.Encrypt(newApiInfo, encryptionKey)
				if err != nil {
					log.Printf("%s : failed to encrypt data for key %s, %v", userID, jsonKey, err)
					updateFailed = append(updateFailed, jsonKey)
					continue
				}
				args = append(args, encryptedData)

			} else if jsonKey == "notifyDest" {
				newWebhook, isStr := value.(string)
				if !isStr {
					log.Printf("%s : got bad value %v for key %s", userID, value, key)
					updateFailed = append(updateFailed, jsonKey)
					continue
				}

				if !webhookIsOK(newWebhook) {
					log.Printf("%s : %s is a non valid webhook", userID, newWebhook)
					updateFailed = append(updateFailed, jsonKey)
					continue
				}

				args = append(args, value)

			} else {
				args = append(args, value)
			}
		case []any:
			strArray := make([]string, len(valType))
			for ind, item := range valType {
				item, isStr := item.(string)
				if !isStr {
					log.Printf("%s : non string element %v in array for key %s", userID, item, jsonKey)
					updateFailed = append(updateFailed, jsonKey)
					continue
				}
				capitalElem := strings.ToUpper(item)
				strArray[ind] = capitalElem
			}
			queryBuilder.WriteString(fmt.Sprintf("%s = $%d::text[], ", key, argCount))
			args = append(args, pq.Array(strArray))
		default:
			log.Printf("%s : got bad type for key %s, type: %s, value: %s", userID, jsonKey, valType, value)
			updateFailed = append(updateFailed, jsonKey)
			continue
		}
		argCount++
	}

	query := strings.TrimSuffix(queryBuilder.String(), ", ") + " WHERE user_id = $1 AND bot_id = $2"
	return args, query, updateFailed, doRestartBot
}

type GetLicenseResponse struct {
	Plan             string  `json:"plan"`
	RenewalTS        int     `json:"renewalTs"`
	AlertWebhook     string  `json:"alertWebhook"`
	RemainingCpytBal Decimal `json:"remainingCpytBal"`
	Err              string  `json:"err"`
}

func (g GetLicenseResponse) String() string {
	return fmt.Sprintf(
		"Plan: %s, RenewalTS: %d, AlertWebhook: %s, RemainingCpytBal: %s, Err: %s",
		g.Plan, g.RenewalTS, g.AlertWebhook, g.RemainingCpytBal, g.Err,
	)
}

// responds with a GetLicenseResponse, derived from User & users table
func onGetLicense(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
) {
	resp := GetLicenseResponse{
		Plan:             "",
		RenewalTS:        0,
		AlertWebhook:     "",
		RemainingCpytBal: decimal.NewFromInt(0),
		Err:              "",
	}

	// get user id
	userID, err := extractUserID(r)
	if err != nil {
		log.Printf("failed to authorise, %v", err)

		resp.Err = "Authorization error"
		sendStructToUser(resp, w, 401)
		return
	}
	log.Printf("%s : received onGetLicense request", userID)

	// query users table
	user, err := db.ReadRecordUsers(userID, pool)
	if err != nil {
		if errors.Is(err, db.ErrNoUser) {
			log.Printf("%s : no active membership", userID)

		} else {
			log.Printf("%s : couldn't get license info, %v", userID, err)

			resp.Err = "Internal server error"
			sendStructToUser(resp, w, 500)
			return
		}
	}

	// send user data back, log
	resp.Plan = user.Plan
	resp.RenewalTS = int(user.RenewalTS.Unix())
	resp.AlertWebhook = user.AlertWebhook
	resp.RemainingCpytBal = user.RemainingCpytBal

	sendStructToUser(resp, w, 200)

	log.Printf("%s : served (%v) to user", userID, resp)
}

type StartBotReq struct {
	BotID int `json:"botID"`
}

type StartBotResp struct {
	Err string `json:"err"`
}

// on receiving a request from user to start their bot
func onStartBot(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	lmc *cls.LaunchingMonitorsCache,
	encryptionKey string,
) {
	resp := StartBotResp{
		Err: "",
	}

	// get user id
	userID, err := extractUserID(r)
	if err != nil {
		log.Printf("failed to authorise, %v", err)

		resp.Err = "Authorization error"
		sendStructToUser(resp, w, 401)
		return
	}

	// decode json body
	var req StartBotReq
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("bad json request, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, 400)
		return
	}

	log.Printf("%s : received start bot request for botID %d", userID, req.BotID)

	// check that bot id exists and is not already running
	oldConfig, err := db.ReadRecordCPYT(pool, userID, req.BotID)
	if err != nil {
		if strings.HasSuffix(err.Error(), "no rows in result set") {
			resp.Err = fmt.Sprintf("Bot %d does not exist", req.BotID)
			sendStructToUser(resp, w, 400)
			log.Printf("%s : cannot start bot %d it doesn't exist", userID, req.BotID)
			return
		}

		log.Printf("%s : unexpected err, %v", userID, err)
		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}
	if oldConfig.BotStatus != db.BotInactive {
		log.Printf("%s : cannot start bot %d, as current status is %s", userID, req.BotID, oldConfig.BotStatus)

		resp.Err = fmt.Sprintf("Bot status is currently %s, cannot start.", oldConfig.BotStatus)
		sendStructToUser(resp, w, 400)
		return
	}

	// decrypt api keys, then create the start bot params
	decryptedKey, err1 := db.Decrypt(oldConfig.APIKeyRaw, encryptionKey)
	decryptedSecret, err2 := db.Decrypt(oldConfig.APISecretRaw, encryptionKey)
	if err1 != nil || err2 != nil {
		log.Printf("%s : failed to decrypt, (%v) (%v)", userID, err1, err2)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	startBotParams := cls.StartBotParam{
		UserID:    userID,
		BotID:     req.BotID,
		Exchange:  oldConfig.Exchange,
		ApiKey:    decryptedKey,
		ApiSecret: decryptedSecret,
		TraderUID: oldConfig.BinanceID,
		Testmode:  oldConfig.TestMode,
		Leverage:  oldConfig.Leverage,
	}

	isServerside, err := startBot(startBotParams, lmc, pool, true)
	if err != nil {
		log.Printf(
			"%s : failed to start bot, isServerside: %t, %v",
			cls.GetBotHash(userID, req.BotID), isServerside, err,
		)

		if isServerside {
			resp.Err = "Internal server error"
			sendStructToUser(resp, w, 500)
			return
		}

		// user error
		resp.Err = err.Error()
		sendStructToUser(resp, w, 400)
		return
	}
	sendStructToUser(resp, w, 200)

	log.Printf("%s : started bot (%d) and served 200 success response", userID, req.BotID)
}

type StopBotReq struct {
	BotID int `json:"botID"`
}

type StopBotResp struct {
	Err string `json:"err"`
}

// on receiving a request from user to stop their bot
func onStopBot(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
) {
	resp := StopBotResp{
		Err: "",
	}

	// get user id
	userID, err := extractUserID(r)
	if err != nil {
		log.Printf("failed to authorise, %v", err)

		resp.Err = "Authorization error"
		sendStructToUser(resp, w, 401)
		return
	}

	// decode json body
	var req StopBotReq
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("bad json request, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, 400)
		return
	}

	log.Printf("%s : received stop bot request for botID %d", userID, req.BotID)

	// check that bot id exists and is running
	oldConfig, err := db.ReadRecordCPYT(pool, userID, req.BotID)
	if err != nil {
		if strings.HasSuffix(err.Error(), "no rows in result set") {
			resp.Err = fmt.Sprintf("Bot %d does not exist", req.BotID)
			sendStructToUser(resp, w, 400)
			log.Printf("%s : cannot stop bot %d it doesn't exist", userID, req.BotID)
			return
		}

		log.Printf("%s : unexpected err, %v", userID, err)
		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}
	if oldConfig.BotStatus != db.BotActive {
		log.Printf("%s : cannot stop bot %d, as current status is %s", userID, req.BotID, oldConfig.BotStatus)

		resp.Err = fmt.Sprintf("Bot status is currently %s, cannot stop.", oldConfig.BotStatus)
		sendStructToUser(resp, w, 400)
		return
	}

	// stop bot
	err = stopBot(userID, req.BotID, oldConfig.BinanceID)
	if err != nil {
		log.Printf("%s : failed to send stop req, %v", userID, err)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	sendStructToUser(resp, w, 200)
	log.Printf("%s : stopped bot (%d) and served 200 success response", userID, req.BotID)
}

type SetUserAlertWebhookRequest struct {
	Webhook string `json:"webhook"`
}

type SetUserAlertWebhookResponse struct {
	Err string `json:"err"`
}

// sets the alert webhook for a user
func onSetUserAlertWebhook(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
) {
	resp := SetUserAlertWebhookResponse{
		Err: "",
	}

	// get user id
	userID, err := extractUserID(r)
	if err != nil {
		log.Printf("failed to authorise, %v", err)

		resp.Err = "Authorization error"
		sendStructToUser(resp, w, 401)
		return
	}

	// decode json body
	var req SetUserAlertWebhookRequest
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("bad json request, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, 400)
		return
	}

	log.Printf("%s : received set user alert webhook request, new webhook: %s", userID, req.Webhook)

	if !webhookIsOK(req.Webhook) {
		msg := fmt.Sprintf("can't update webhook, (%s) is a bad webhook", req.Webhook)
		log.Printf("%s : %s", userID, msg)

		resp.Err = msg
		sendStructToUser(resp, w, 400)

		return
	}

	// update webhook
	err = db.UpdateUserAlertWebhook(userID, req.Webhook, pool)
	if err != nil {
		if errors.Is(err, db.ErrNoUser) {
			log.Printf("%s : can't set user alert webhook, no user found", userID)

			resp.Err = "can't update, no existing user found"
			sendStructToUser(resp, w, 400)
			return
		}

		// unknown err
		log.Printf("%s : unknown err setting user alert webhook, %v", userID, err)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	log.Printf("%s : successfully set user alert webhook", userID)

	sendStructToUser(resp, w, 200)
}

type BotLog struct {
	Ts  int64  `json:"ts"`
	Msg string `json:"msg"`
}

type GetLogsResp struct {
	Logs []BotLog `json:"logs"` // this should already be serialized by ls-core
	Err  string   `json:"err"`
}

// get logs for a user
// expects format of: \getLogs?botID=idhere
// TODO #36 add tests for onGetLogs
func onGetLogs(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
) {
	resp := GetLogsResp{
		Logs: []BotLog{},
		Err:  "",
	}

	// get user id
	userID, err := extractUserID(r)
	if err != nil {
		log.Printf("failed to authorise, %v", err)

		resp.Err = "Authorization error"
		sendStructToUser(resp, w, 401)
		return
	}

	// extract botId from query parameters
	queryParams := r.URL.Query()
	botIDStr := queryParams.Get("botID")
	if botIDStr == "" {
		log.Printf("%s : missing botID param in get logs request", userID)

		resp.Err = "Missing botId in query parameters"
		sendStructToUser(resp, w, 400)
		return
	}

	botID, err := strconv.Atoi(botIDStr)
	if err != nil {
		log.Printf("%s : invalid botID value (%s) in get logs request", userID, botIDStr)

		resp.Err = "Invalid botID value in query parameters"
		sendStructToUser(resp, w, 400)
		return
	}

	log.Printf("%s : received get logs request for botID: %d", userID, botID)

	// get binance ID for the user
	bc, err := db.ReadRecordCPYT(pool, userID, botID)
	if err != nil {
		log.Printf("%s : failed to read db for user, %v", userID, err)

		status := 500                      // default
		resp.Err = "Internal server error" // default

		if strings.HasSuffix(err.Error(), "no rows in result set") {
			resp.Err = "Bot doesn't exist"
			status = 400
		}

		sendStructToUser(resp, w, status)
		return
	}
	binanceID := bc.BinanceID
	botHash := cls.GetBotHash(userID, botID)

	if binanceID == "" {
		msg := "binanceID is empty"
		log.Printf(
			"%s : can't return logs for botID %d, %s",
			userID, botID, msg,
		)
		resp.Err = msg
		sendStructToUser(resp, w, 500)
		return
	}

	// find monitor
	mon, exists := handler.getMonitor(binanceID)
	if !exists {
		log.Printf("GetLogs : no monitor for uid %s, from bothash %s", binanceID, botHash)
		resp.Err = "Couldn't find bot logs, no monitor exists."
		sendStructToUser(resp, w, 400)
		return
	}

	// get bot off monitor
	bot, exists := mon.getBot(botHash)
	if !exists {
		log.Printf("GetLogs : no bot %s on uid %s", botHash, binanceID)
		resp.Err = "Couldn't find bot logs, no bot exists."
		sendStructToUser(resp, w, 400)
		return
	}

	// get logs from bot
	logs, err := bot.Logs.Get()
	if err != nil {
		log.Printf("GetLogs : failed to get logs off of bot, %v", err)
		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	// unmarshall the logs string
	var logsData []BotLog
	err = json.Unmarshal([]byte(logs), &logsData)
	if err != nil {
		log.Printf("%s : failed to unmarshal logs JSON string, %v", userID, err)
		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	// set the Logs field to the unmarshalled data
	resp.Logs = logsData
	sendStructToUser(resp, w, 200)

	log.Printf("%s : successfully served logs for botID %d", userID, botID)
}
