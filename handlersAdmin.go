package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lightspeed-trading/app/db"
)

// add a plan to a specific userID. The userID must not exist in
// users table already. For admin use only. Returns status 401 if
// auth is invalid.
func onAdminAddPlanToUser(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	expectedAdminKey string,
) {
	// check user has admin api key
	key := r.Header.Get("LS-API-Key")
	if key != expectedAdminKey {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}

	// decode json body
	var req SetUserPlanReq
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("bad json request, %v\n json: %s", err, r.Body)
		sendStructToUser(
			SetUserPlanResp{
				Err: "Badly formatted request",
			}, w, 400,
		)
		return
	}

	status, err := SetUserPlan(req, pool)

	if status == 200 {
		log.Printf("ADMIN : added plan to user %s", req.TargetUserID)
		sendStructToUser(
			SetUserPlanResp{
				Err: "",
			}, w, 200,
		)

	} else if status == 400 {
		log.Printf("ADMIN : failed (400) to add plan for user %s, %v", req.TargetUserID, err)
		sendStructToUser(
			SetUserPlanResp{
				Err: err.Error(),
			}, w, 400,
		)

	} else {
		log.Printf("ADMIN : failed (%d) to add plan for user %s, %v", status, req.TargetUserID, err)
		sendStructToUser(
			SetUserPlanResp{
				Err: "Internal server error",
			}, w, status,
		)
	}
}

type SetUserPlanReq struct {
	TargetUserID      string `json:"targetUserID"`
	PlanName          string `json:"planName"`
	OverwriteExisting bool   `json:"overwriteExisting"`
	DaysUntilRenewal  *int   `json:"daysUntilRenewal"`
}

type SetUserPlanResp struct {
	Err string `json:"err"`
}

// Returns status, error. If status == 400: err must be showable to user
// if req.DaysUntilRenewal == nil, it will use the users existing
func SetUserPlan(req SetUserPlanReq, pool *pgxpool.Pool) (int, error) {
	// handle pre existing users
	userAlertWebhook := ""

	// set at an impossible value, so we can check that its been initialised correctly.
	// And so we can make sure that if nil DaysUntilRenewal is passed, the user does
	// indeed exist.
	defaultDaysLeft := -999
	daysLeft := defaultDaysLeft
	var oldPlan db.Plan

	user, err := db.ReadRecordUsers(req.TargetUserID, pool)

	userAlreadyHasPlan := user.Plan != ""
	unexpectedErr := err != nil && !errors.Is(err, db.ErrNoUser)

	if unexpectedErr {
		return 500, fmt.Errorf("failed to read from plans, %v", err)
	}

	if userAlreadyHasPlan {
		// user exists & has a plan
		if !req.OverwriteExisting {
			return 400, fmt.Errorf("user %s already has a plan %s", req.TargetUserID, user.Plan)
		}

		if req.DaysUntilRenewal == nil {
			diff := time.Until(user.RenewalTS.In(time.UTC))

			daysFloat := float64(diff) / float64(time.Hour*24)
			daysRoundedUp := int(math.Ceil(daysFloat))

			daysLeft = daysRoundedUp
		} else {
			daysLeft = *req.DaysUntilRenewal
		}

		// read users old plan
		oldPlan, err = db.ReadPlan(user.Plan, pool)
		if err != nil {
			if strings.HasSuffix(err.Error(), "no rows in result set") {
				return 400, fmt.Errorf("users old plan %s doesn't exist", user.Plan)
			}
			return 400, fmt.Errorf("failed to read users old plan, %v", err)
		}

		// wipe user data
		userAlertWebhook = user.AlertWebhook
		err = db.WipeUser(pool, req.TargetUserID, true, false)
		if err != nil {
			return 500, fmt.Errorf("failed to wipe from users, %v", err)
		}

	} else if req.DaysUntilRenewal == nil {
		// admin wants to use the users previous days remaining, but the
		// user didn't previously exist. This is impossible.
		return 400, fmt.Errorf(
			"can't use the users previous days remaining when updating their plan if they didn't previously exist",
		)

	} else {
		daysLeft = *req.DaysUntilRenewal
	}

	// read from plans
	newPlan, err := db.ReadPlan(req.PlanName, pool)
	if err != nil {
		if strings.HasSuffix(err.Error(), "no rows in result set") {
			return 400, fmt.Errorf("plan (%s) doesn't exist", req.PlanName)
		}
		return 500, fmt.Errorf("failed to read from plans, %v", err)
	}

	// write to users
	daysToAdd := time.Hour * 24 * time.Duration(daysLeft)
	newRenewalTime := time.Now().Add(daysToAdd).In(time.UTC)

	balanceInBots := oldPlan.MaxCTBalance.Sub(user.RemainingCpytBal)
	newRemainingBalance := newPlan.MaxCTBalance.Sub(balanceInBots)

	err = db.WriteRecordUsers(
		db.User{
			UserID:           req.TargetUserID,
			Plan:             req.PlanName,
			RenewalTS:        newRenewalTime,
			AlertWebhook:     userAlertWebhook,
			RemainingCpytBal: newRemainingBalance,
		},
		pool,
	)
	if err != nil {
		return 500, fmt.Errorf("failed to write to users, %v", err)
	}

	// write to copytrading
	status, err := SetNumInstances(req.TargetUserID, newPlan.AllowedCTInstances, pool)
	if err != nil {
		return status, err
	}
	log.Printf(
		"ADMIN : successfully set instance count to %d for %s",
		newPlan.AllowedCTInstances, req.TargetUserID,
	)

	return 200, nil
}

// status int : 200 success, 400 user error, 500 our error
func SetNumInstances(userID string, numInstances int, pool *pgxpool.Pool) (int, error) {
	// work out if we need to increase or decrease number of instances
	instances, err := db.ReadAllRecordsCPYT(pool, userID)
	if err != nil {
		return 500, fmt.Errorf("failed to read all records, %v", err)
	}
	existingInstanceCount := len(instances)

	if existingInstanceCount > numInstances {
		// delete some of them
		deleteCount := existingInstanceCount - numInstances
		err := deleteInstances(userID, existingInstanceCount, deleteCount, pool)
		if err != nil {
			return 500, fmt.Errorf(
				"failed to delete some of the users instances, total: %d delete: %d, %v",
				existingInstanceCount, deleteCount, err,
			)
		}

	} else if existingInstanceCount < numInstances {
		// add to their instances
		for i := existingInstanceCount + 1; i <= numInstances; i++ {
			err := db.WriteRecordCPYT(
				pool,
				db.NewBotConfig(userID, i),
			)
			if err != nil {
				return 500, fmt.Errorf(
					"%s : failed to write to copytrading at iter %d, %v",
					"ADMIN", i, err,
				)
			}
			log.Printf("%s : successfully added new bot %s_%d", "ADMIN", userID, i)
		}

	} else {
		// no change required
		log.Printf("%s : user already had %d instances", "ADMIN", numInstances)
	}

	return 200, nil
}

// delete from copytrading a subset of the users instances
func deleteInstances(
	userID string,
	totalCount int, // number of instances they have in total
	deleteCount int, // number to delete
	pool *pgxpool.Pool,
) error {
	lastBotIDToDelete := totalCount - deleteCount + 1

	for i := totalCount; i >= lastBotIDToDelete; i-- {
		_, err := pool.Exec(
			context.Background(),
			"DELETE FROM copytrading WHERE user_id=$1 AND bot_id=$2",
			userID, i,
		)
		if err != nil {
			return fmt.Errorf(
				"failed to delete bot instance %d for user %s, %v",
				i, userID, err,
			)
		}
		log.Printf("ADMIN : successfully deleted %s_%d", userID, i)
	}

	return nil
}

type CreateNewPlanReq struct {
	Plan              db.Plan `json:"plan"`
	OverwriteExisting bool    `json:"overwriteExisting"`
}

type CreateNewPlanResp struct {
	Err string `json:"err"`
}

// create a new plan
// needs admin key
func onAdminCreateNewPlan(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	expectedAdminKey string,
) {
	// check user has admin api key
	key := r.Header.Get("LS-API-Key")
	if key != expectedAdminKey {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}

	resp := CreateNewPlanResp{
		Err: "",
	}

	// decode json body
	var req CreateNewPlanReq
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("ADMIN : bad json request, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, 400)
		return
	}

	log.Printf(
		"ADMIN : received request with overwrite %t to create plan %s",
		req.OverwriteExisting, req.Plan,
	)

	_, err = db.ReadPlan(req.Plan.Name, pool)
	if err != nil {
		if !strings.HasSuffix(err.Error(), "no rows in result set") {
			// unexpected error
			log.Printf("ADMIN : failed to read plans, %v", err)

			resp.Err = "Internal server error"
			sendStructToUser(resp, w, 500)
			return
		}
	} else {
		// plan already exists
		if !req.OverwriteExisting {
			log.Printf(
				"ADMIN : plan %s already exists, overwrite %t, aborting",
				req.Plan.Name, req.OverwriteExisting,
			)
			resp.Err = "plan already exists"
			sendStructToUser(resp, w, 400)
			return
		}

		// delete plan
		_, err = pool.Exec(context.Background(), "DELETE FROM plans WHERE name=$1", req.Plan.Name)
		if err != nil {
			log.Printf("ADMIN : failed to delete existing plan, %v", err)

			resp.Err = "Internal server error"
			sendStructToUser(resp, w, 500)
			return
		}
		log.Printf("ADMIN : successfully deleted existing plan %s", req.Plan.Name)
	}

	// insert plan
	_, err = pool.Exec(
		context.Background(),
		"INSERT INTO plans (name, cost_per_month, comment, allowed_ct_instances, allowed_sign_instances, max_ct_balance, displayable_name, is_sold, initial_payment) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		req.Plan.Name, req.Plan.CostPerMonth, req.Plan.Comment,
		req.Plan.AllowedCTInstances, req.Plan.AllowedSignInstances,
		req.Plan.MaxCTBalance, req.Plan.DisplayableName, req.Plan.IsSold,
		req.Plan.InitialPayment,
	)
	if err != nil {
		log.Printf("ADMIN : failed to insert new plan, %v", err)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}
	log.Printf("ADMIN : successfully inserted new plan %s", req.Plan.Name)

	sendStructToUser(resp, w, 200)
}

type DeletePlanReq struct {
	PlanName string `json:"planName"`
}

type DeletePlanResp struct {
	Err string `json:"err"`
}

// delete a plan by its name
func onAdminDeletePlan(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	expectedAdminKey string,
) {
	// check user has admin api key
	key := r.Header.Get("LS-API-Key")
	if key != expectedAdminKey {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}

	resp := DeletePlanResp{
		Err: "",
	}

	// decode json body
	var req DeletePlanReq
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		log.Printf("ADMIN : bad json request, %v\n json: %s", err, r.Body)

		resp.Err = "Badly formatted request"
		sendStructToUser(resp, w, 400)
		return
	}

	log.Printf("ADMIN : received request to delete plan %s", req.PlanName)

	// delete plan
	_, err = pool.Exec(context.Background(), "DELETE FROM plans WHERE name=$1", req.PlanName)
	if err != nil {
		log.Printf("ADMIN : failed to delete plan, %v", err)

		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)
		return
	}

	log.Printf("ADMIN : successfully deleted plan %s", req.PlanName)
	sendStructToUser(resp, w, 200)
}
