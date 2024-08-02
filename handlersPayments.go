package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgxpool"

	cls "github.com/lightspeed-trading/app/classes"
	"github.com/lightspeed-trading/app/common"
)

// runs when a payment has been sent and backend needs to confirm it
// request body must be a PaymentSentRequest json
func onPaymentSent(
	w http.ResponseWriter,
	r *http.Request,
	ourWalletAddr string,
	payments *cls.PaymentsHdlr,
	dbPool *pgxpool.Pool,
	ethCli *ethclient.Client,
) {
	resp := cls.PaymentSentResponse{
		VerificationInProgress: false,
		Err:                    "",
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
	var req cls.PaymentSentRequest
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("failed to decode json, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, 400)
		return
	}
	log.Printf("%s : received onPaymentSent request (%v)", userID, req)

	// check if payment is elligible to be claimed by this user
	claimed, reasonForFailure, err := TryClaimTx(payments, userID, req.TxID, ethCli)
	if err != nil {
		log.Printf("%s : failed to claim tx (our error), %v", userID, err)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}
	if !claimed {
		log.Printf("%s : user cannot claim payment. %s", userID, reasonForFailure)

		resp.Err = "Can't verify payment"
		sendStructToUser(resp, w, 400)
		return
	}
	resp.VerificationInProgress = true
	sendStructToUser(resp, w, 200)

	log.Printf("%s : received valid payment request, now verifying…", userID)

	// verify payment
	go func() {
		success := VerifyPayment(payments, userID, req, ourWalletAddr, dbPool, ethCli)
		if !success {
			return // do nothing if verification fails
		}

		var err error
		if req.IsRenewal {
			err = StoreDataOnRenewalPayment(userID, dbPool)
		} else {
			err = storeDataOnInitialPayment(userID, req.Plan, dbPool)
		}

		if err != nil {
			// create error message
			var errMsg string
			if errors.Is(err, ErrCantRenewNonexistantUser) {
				errMsg = fmt.Sprintf(
					"%s : failed to renew, user does not have any existing plans", userID,
				)
			} else {
				errMsg = fmt.Sprintf(
					"%s : failed to store data on successful payment (isRenewal: %t), %v",
					userID, req.IsRenewal, err,
				)
			}
			log.Print(errMsg)

			// send a staff alert as well
			err = common.SendStaffAlert(errMsg)
			if err != nil {
				log.Printf("%s : failed to send a staff alert, %v", userID, err)
			}
		}
	}()
}

type CheckPaymentStatusRequest struct {
	Plan string `json:"planName"`
	TxID string `json:"txID"`
}

type CheckPaymentStatusResponse struct {
	Status string `json:"status"`
	Err    string `json:"err"`
}

func onCheckPaymentStatus(
	w http.ResponseWriter,
	r *http.Request,
	payments *cls.PaymentsHdlr,
) {
	resp := CheckPaymentStatusResponse{
		Status: "null",
		Err:    "",
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
	var req cls.PaymentSentRequest
	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("bad json request, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, 400)
		return
	}

	// check status
	payments.Mu.Lock()
	defer payments.Mu.Unlock()

	key := getPaymentsKey(req.TxID)
	payment, exists := payments.Payments[key]

	log.Printf("%s : received check payment status request for txID %s", userID, req.TxID)

	if exists && payment.UserID != userID {
		// payment is claimed by a different user
		resp.Err = "This payment is claimed by another user."
		sendStructToUser(resp, w, 400)

		log.Printf("%s : payment for %s is claimed by a different user %s", userID, req.TxID, payment.UserID)
		return
	}

	// parse status from payment
	removeAfter := false
	if !exists {
		// untracked payment
	} else if !payment.IsSuccess && payment.ErrStr == "" {
		// pending payment
		resp.Status = "pending"
	} else if payment.IsSuccess {
		// successful payment
		resp.Status = "success"
		removeAfter = true
	} else {
		// failed payment
		resp.Status = "fail"
		resp.Err = payment.ErrStr
		removeAfter = true
	}

	// remove from payments handler if success or fail
	if removeAfter {
		delete(payments.Payments, key)
	}

	// send to user
	sendStructToUser(resp, w, 200)

	log.Printf("%s : served status %s to user with err (%v)", userID, resp.Status, resp.Err)
}
