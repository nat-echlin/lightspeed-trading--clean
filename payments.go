package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	dwh "github.com/nat-echlin/dwhooks"

	"github.com/jackc/pgx/v5/pgxpool"
	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
	db "github.com/lightspeed-trading/app/db"
)

var ErrCantRenewNonexistantUser = errors.New("can't renew a user who hasn't already paid their initial")

func getPaymentsKey(txID string) string {
	return txID
}

// Adds data to tables after a user has paid for a copytrading plan.
// If err is returned, user should be instructed to contact support.
func storeDataOnInitialPayment(
	userID string,
	planName string,
	dbpool *pgxpool.Pool,
) error {
	// read plans table
	userPlan, err := db.ReadPlan(planName, dbpool)
	if err != nil {
		return fmt.Errorf("failed to read plans table, %v", err)
	}

	// insert user data into users table
	user := db.User{
		UserID:           userID,
		Plan:             planName,
		RenewalTS:        time.Now().UTC().AddDate(0, 1, 0),
		AlertWebhook:     "",
		RemainingCpytBal: userPlan.MaxCTBalance,
	}
	err = db.WriteRecordUsers(user, dbpool)
	if err != nil {
		return fmt.Errorf("failed to write to users table, %v", err)
	}

	// acquire conn from pool
	conn, err := dbpool.Acquire(context.Background())
	if err != nil {
		return fmt.Errorf("failed to acquire pool, %v", err)
	}
	defer conn.Release()

	// create as many copytrading instances as the user has allowed
	for iter := 1; iter <= userPlan.AllowedCTInstances; iter++ {
		err := db.WriteRecordCPYT(
			dbpool,
			db.NewBotConfig(userID, iter),
		)
		if err != nil {
			return fmt.Errorf("%s : failed to insert new bot into users table at iter %d, %v", userID, iter, err)
		}
	}

	return nil
}

// store data after a user renews their plan
// does not return, instead logs and handles all issues locally
func StoreDataOnRenewalPayment(
	userID string,
	dbPool *pgxpool.Pool,
) error {
	// check that the user already exists
	_, err := db.ReadRecordUsers(userID, dbPool)
	if err != nil {
		if strings.HasSuffix(err.Error(), "no rows in result set") {
			return ErrCantRenewNonexistantUser
		}
		return fmt.Errorf("failed to read from users table, %v", err)
	}

	// add a month to the user
	err = db.AddToUserRenewalDate(userID, "1 month", dbPool)
	if err != nil {
		return fmt.Errorf("failed to update renewal timestamp, %v", err)
	}

	return nil
}

// check if a payment was a success or not. If err != nil, the payment
// was a success. The returned float64 is the value in eth of the
// tx. Will be a long running function as it loops checking until
// the tx has either succeeded or failed.
func checkPaymentSuccess(
	txID string,
	ourWalletAddr string,
	userID string,
	ethCli *ethclient.Client,
) (float64, error) {
	txHash := ethcommon.HexToHash(txID)

	// check transaction status every tick until timeout
	timeout := time.After(5 * time.Minute)
	tick := time.Tick(10 * time.Second)

	for {
		select {
		case <-tick:
			log.Printf("%s : checking tx %s…", userID, txID)

			tx, pending, err := ethCli.TransactionByHash(context.Background(), txHash)
			if err != nil {
				log.Printf("%s : failed to retrieve transaction: %v", userID, err)
				continue
			}

			if pending {
				log.Printf("%s : tx %s is pending", userID, txID)
				continue
			}

			// check the recipient
			recipietAddr := tx.To().Hex()
			if recipietAddr == ourWalletAddr {
				// find how much we received after tx success
				valueWei := tx.Value()
				valueEthBigFloat := new(big.Float).Quo(new(big.Float).SetInt(valueWei), big.NewFloat(math.Pow10(18)))
				valueEth, _ := valueEthBigFloat.Float64()

				log.Printf("%s : tx success, we receieved %.5f eth", userID, valueEth)
				return valueEth, nil

			} else {
				log.Printf(
					"%s : payment has gone to an incorrect wallet address %s instead of ours %s",
					userID,
					recipietAddr,
					ourWalletAddr,
				)
				return 0, errors.New("payment sent to the incorrect wallet")
			}

		case <-timeout:
			log.Printf("%s : payment timeout", userID)
			return 0, fmt.Errorf("payment timeout, took too long to be confirmed")
		}
	}
}

type CryptoData struct {
	Ethereum struct {
		USD float64 `json:"usd"`
	} `json:"ethereum"`
}

func getEthPrice() (float64, error) {
	response, err := http.Get("https://api.coingecko.com/api/v3/simple/price?ids=ethereum&vs_currencies=usd")
	if err != nil {
		return 0, fmt.Errorf("err making api request, %v", err)
	}

	defer response.Body.Close()

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, fmt.Errorf("err reading response body, %v", err)
	}

	var cryptoData CryptoData

	err = json.Unmarshal(data, &cryptoData)
	if err != nil {
		return 0, fmt.Errorf("err when unmarshalling response body: %v", err)
	}

	return cryptoData.Ethereum.USD, nil
}

// verify if a payment has been successful. Returns true if success,
// false otherwise. Payment must already be in the handler.
func VerifyPayment(
	payments *cls.PaymentsHdlr,
	userID string,
	req cls.PaymentSentRequest,
	ourWalletAddr string,
	dbPool *pgxpool.Pool,
	ethCli *ethclient.Client,
) bool {
	// check if the payment has gone through
	qtyEthReceived, err := checkPaymentSuccess(req.TxID, ourWalletAddr, userID, ethCli)

	// update payments handler
	payments.Mu.Lock()
	defer payments.Mu.Unlock()

	key := getPaymentsKey(req.TxID)
	paymentWithData := payments.Payments[key]
	paymentWithData.ReceivedQtyEth = qtyEthReceived

	if err != nil {
		paymentWithData.ErrStr = err.Error()
		payments.Payments[key] = paymentWithData
		return false
	}

	// get eth price
	ethPrice, err := getEthPrice()
	if err != nil {
		log.Printf("%s : failed to get eth price, %v", userID, err)

		paymentWithData.ErrStr = "Internal server error"
		payments.Payments[key] = paymentWithData
		return false
	}

	// get plan, work out how much usd we want to receive
	p, err := db.ReadPlan(req.Plan, dbPool)
	if err != nil {
		log.Printf("%s : failed to read plan (%s), %v", userID, req.Plan, err)

		paymentWithData.ErrStr = "Internal server error"
		payments.Payments[key] = paymentWithData
		return false
	}

	expectedUSD := p.CostPerMonth
	if !req.IsRenewal {
		expectedUSD += p.InitialPayment
	}

	// verify quantity received is satisfactory
	usdReceived := qtyEthReceived * ethPrice
	if usdReceived < expectedUSD*0.95 {
		// insufficient quantity
		log.Printf("%s : got an insufficient payment, expected: %f, got: %f (usd)", userID, expectedUSD, usdReceived)

		paymentWithData.ErrStr = "Insufficient payment received"
		payments.Payments[key] = paymentWithData
		return false
	}

	// sufficient qty receievd
	paymentWithData.IsSuccess = true
	payments.Payments[key] = paymentWithData

	return true
}

// check whether a tx id is ok to start verifying. If it is, it will be
// added to the handler. If not, string returned will be the reason
// why its not elligible.
func TryClaimTx(
	payments *cls.PaymentsHdlr,
	userID string,
	txID string,
	ethCli *ethclient.Client,
) (bool, string, error) {
	payments.Mu.Lock()
	defer payments.Mu.Unlock()

	paymentsKey := getPaymentsKey(txID)

	_, exists := payments.Payments[paymentsKey]
	if exists {
		return false, "Payment already claimed by a different user.", nil
	}

	// check if the transaction has already been completed
	txHash := ethcommon.HexToHash(txID)

	numRetries := 3
	waitDuration := 2 * time.Second
	var isPending bool
	var err error

	for i := 1; i <= numRetries; i++ {
		_, isPending, err = ethCli.TransactionByHash(context.Background(), txHash)
		if err == nil {
			log.Printf("%s : success getting tx, pending: %v", userID, isPending)
			break // if it was a success getting the tx
		}

		log.Printf("%s : failed to get tx, err: %v", userID, err)
		if i < numRetries {
			time.Sleep(waitDuration)
		}
	}
	if err != nil {
		return false, "", fmt.Errorf("failed to get tx, %v", err)
	}

	if !isPending {
		// // payment has already been completed and (in theory) claimed by another user
		// // but allow if <10s since it has been completed

		// // get the transaction receipt, which includes the block number
		// receipt, err := ethCli.TransactionReceipt(context.Background(), txHash)
		// if err != nil {
		// 	return false, "", fmt.Errorf("failed to get transaction receipt, %v", err)
		// }

		// // get the block, which includes the timestamp
		// block, err := ethCli.BlockByNumber(context.Background(), receipt.BlockNumber)
		// if err != nil {
		// 	return false, "", fmt.Errorf("failed to get block, %v", err)
		// }

		// timeSinceMined := time.Now().Unix() - int64(block.Time())
		// if timeSinceMined < 10 {
		// 	return false, "Payment cannot be claimed", nil
		// }
		return false, "Payment cannot be claimed", nil
	}

	payments.Payments[paymentsKey] = cls.Payment{
		UserID:         userID,
		TxID:           txID,
		IsSuccess:      false,
		ReceivedQtyEth: 0,
		ErrStr:         "",
	}

	return true, "", nil
}

// gets a []NotificationUser , users who have their renewal dates 3, 2, or
// 1 day away, or those who's renewal dates have passed.
func getUsersForNotification(pool *pgxpool.Pool) ([]cls.NotificationUser, error) {
	const query = `
        SELECT user_id, plan, renewal_ts, alert_webhook, 
			CASE 
				WHEN (current_timestamp - renewal_ts) >= interval '0 days'  THEN -1
				WHEN renewal_ts - current_timestamp BETWEEN interval '3 days' AND interval '3 days 1 hour' THEN 3
				WHEN renewal_ts - current_timestamp BETWEEN interval '2 days' AND interval '2 days 1 hour' THEN 2
				WHEN renewal_ts - current_timestamp BETWEEN interval '1 days' AND interval '1 days 1 hour' THEN 1
			END AS days_away
        FROM users
        WHERE (current_timestamp - renewal_ts) >= interval '0 days' 
           OR (renewal_ts - current_timestamp) BETWEEN interval '3 days' AND interval '3 days 1 hour'
           OR (renewal_ts - current_timestamp) BETWEEN interval '2 days' AND interval '2 days 1 hour'
           OR (renewal_ts - current_timestamp) BETWEEN interval '1 days' AND interval '1 days 1 hour'
    `
	rows, err := pool.Query(context.Background(), query)
	if err != nil {
		return nil, fmt.Errorf("failed to query users, %v", err)
	}
	defer rows.Close()

	usersWithApproachingRenewals := []cls.NotificationUser{}
	for rows.Next() {
		var user cls.NotificationUser
		err := rows.Scan(&user.UserID, &user.Plan, &user.RenewalTS, &user.AlertWebhook, &user.DaysAway)
		if err != nil {
			return nil, err
		}
		usersWithApproachingRenewals = append(usersWithApproachingRenewals, user)
	}

	if len(usersWithApproachingRenewals) == 0 {
		return usersWithApproachingRenewals, nil
	}

	// get plans, to add renewal costs into the []NotificationUser
	plans, err := db.ReadAllPlans(pool)
	if err != nil {
		return nil, fmt.Errorf("failed to query plans, %v", err)
	}

	planLookup := map[string]float64{}
	for _, p := range plans {
		planLookup[p.Name] = p.CostPerMonth
	}

	users := make([]cls.NotificationUser, len(usersWithApproachingRenewals))
	for ind, u := range usersWithApproachingRenewals {
		u.ExpectedRenewal = planLookup[u.Plan]
		users[ind] = u
	}

	return users, rows.Err()
}

// gets a list of users who have their renewals approaching, then sends
// notifications to users who are close to their date. If their date
// has passed, their user data will be wiped.
func handleApproachingRenewalDates(pool *pgxpool.Pool) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		<-ticker.C

		notifyList, err := getUsersForNotification(pool)
		if err != nil {
			errMsg := fmt.Sprintf("error getting users for renewal actions, %v", err)
			log.Print(errMsg)
			err = common.SendStaffAlert(errMsg)
			if err != nil {
				log.Printf("failed to send staff alert, %v", err)
			}
			continue
		}

		for _, user := range notifyList {
			go func(user cls.NotificationUser) {
				handleIndividualApproachingRenewal(user, pool)
			}(user)
		}
	}
}

// handles all errors locally
func handleIndividualApproachingRenewal(user cls.NotificationUser, pool *pgxpool.Pool) {
	// TODO #3 handleIndividualApproachingRenewal needs to handle empty user.AlertWebhook
	if user.AlertWebhook == "" {
		foundWebhook, err := findWebhookForUser(user.UserID, pool)
		if err != nil {
			common.LogAndSendAlertF(
				"%s : failed to find webhook for user with no user.AlertWebhook, %v",
				user.UserID, err,
			)
		} else {
			user.AlertWebhook = foundWebhook
		}
	}

	// send a renewal noti to staff disc that a user is 1 day away from renewal
	if user.DaysAway == 1 {
		msg := fmt.Sprintf("userID %s only has 1 day left on their renewal, notifications have been sent already",
			user.UserID,
		)

		dwh.NewWebhook(STAFF_RENEWAL_DISC_WH_URL).Send(
			dwh.NewMessage(msg),
		)
	}

	// check if their renewal period is over
	if user.DaysAway == -1 {
		err := db.WipeUser(pool, user.UserID, true, true)
		if err != nil {
			log.Printf("%s : failed to wipe user, %v", user.UserID, err)
			return
		}

		msg := fmt.Sprintf("%s : successfully wiped user, renewal period ended", user.UserID)

		log.Print(msg)
		dwh.NewWebhook(STAFF_RENEWAL_DISC_WH_URL).Send(dwh.NewMessage(msg))

		return
	}

	// still time in their renewal period, need to send a notification
	err := common.SendRenewalNoti(user)
	if err != nil {
		common.LogAndSendAlertF(
			"%s : failed to send renewal notification for %d day(s) left, %v",
			user.UserID, user.DaysAway, err,
		)
	}
}

// look through a users configured bots to find a valid webhook that they've
// set up. Used for when a user needs a notification sent to them, but has
// no webhook configured in their user record.
func findWebhookForUser(userID string, pool *pgxpool.Pool) (string, error) {
	bots, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		return "", fmt.Errorf("failed to read copytrading, %v", err)
	}

	for _, b := range bots {
		if b.NotifyDest != "" {
			return b.NotifyDest, nil
		}
	}
	return "", errors.New("no bots have a configured webhook")
}
