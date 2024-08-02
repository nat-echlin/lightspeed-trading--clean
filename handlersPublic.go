package main

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lightspeed-trading/app/db"
)

type PlanUI struct {
	Name                 string  `json:"name"`
	CostPerMonth         float64 `json:"costPerMonth"`
	AllowedCTInstances   int     `json:"allowedCTInstances"`
	AllowedSignInstances int     `json:"allowedSignInstances"`
	MaxCTBalance         Decimal `json:"maxCTBalance"`
	DisplayableName      string  `json:"displayableName"`
	InitialPayment       float64 `json:"initialPayment"`
}

type GetPlansResp struct {
	Plans []PlanUI `json:"plans"`
	Err   string   `json:"err"`
}

// return a list of plans to the user
// does not need auth
func onGetPlans(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
) {
	resp := GetPlansResp{
		Plans: []PlanUI{},
		Err:   "",
	}

	// read all plans
	plans, err := db.ReadAllPlans(pool)
	if err != nil {
		resp.Err = "Internal server error"
		sendStructToUser(resp, w, 500)

		log.Printf("failed to read all plans, %v", err)
		return
	}

	log.Printf("NO AUTH : received get plans request")

	// parse each db.Plan into a PlanUI, ignoring ones that aren't sold
	sentPlans := ""
	for _, p := range plans {
		if !SEND_TEST_PLANS && !p.IsSold {
			continue
		}
		pUI := PlanUI{
			Name:                 p.Name,
			CostPerMonth:         p.CostPerMonth,
			AllowedCTInstances:   p.AllowedCTInstances,
			AllowedSignInstances: p.AllowedSignInstances,
			MaxCTBalance:         p.MaxCTBalance,
			DisplayableName:      p.DisplayableName,
			InitialPayment:       p.InitialPayment,
		}
		resp.Plans = append(resp.Plans, pUI)
		sentPlans += pUI.Name + " "
	}

	log.Printf(
		"NO AUTH : served %d plans to user with SEND_TEST_PLANS: %t, (%s)",
		len(resp.Plans), SEND_TEST_PLANS, sentPlans,
	)

	sendStructToUser(resp, w, 200)
}
