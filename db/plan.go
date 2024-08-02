package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type Plan struct {
	Name                 string          `json:"name"`
	CostPerMonth         float64         `json:"costPerMonth"`
	Comment              string          `json:"comment"`
	AllowedCTInstances   int             `json:"allowedCTInstances"`
	AllowedSignInstances int             `json:"allowedSignInstances"`
	MaxCTBalance         decimal.Decimal `json:"maxCTBalance"`
	DisplayableName      string          `json:"displayableName"`
	IsSold               bool            `json:"isSold"`
	InitialPayment       float64         `json:"initialPayment"`
}

func (p Plan) String() string {
	return fmt.Sprintf(
		"Plan {%s (%s), CostPerMonth: %.2f, Comment: %s, CT: %d, SIGN: %d, CTBal: %d, IsSold: %t, Initial: %.2f}",
		p.Name, p.DisplayableName, p.CostPerMonth, p.Comment,
		p.AllowedCTInstances, p.AllowedSignInstances, p.MaxCTBalance,
		p.IsSold, p.InitialPayment,
	)
}

func ReadPlan(planName string, pool *pgxpool.Pool) (Plan, error) {
	var plan Plan

	// read from db
	row := pool.QueryRow(
		context.Background(),
		"SELECT * FROM plans WHERE name=$1",
		planName,
	)

	err := row.Scan(
		&plan.Name, &plan.CostPerMonth, &plan.Comment, &plan.AllowedCTInstances,
		&plan.AllowedSignInstances, &plan.MaxCTBalance, &plan.DisplayableName,
		&plan.IsSold, &plan.InitialPayment,
	)
	if err != nil {
		return plan, fmt.Errorf("failed to scan row, %v", err)
	}

	return plan, nil
}

func WriteRecordPlans(plan Plan, pool *pgxpool.Pool) error {
	_, err := pool.Exec(
		context.Background(),
		`INSERT INTO plans (name, cost_per_month, comment, allowed_ct_instances, allowed_sign_instances, max_ct_balance, displayable_name, is_sold, initial_payment) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		plan.Name, plan.CostPerMonth, plan.Comment, plan.AllowedCTInstances, plan.AllowedSignInstances, plan.MaxCTBalance, plan.DisplayableName, plan.IsSold, plan.InitialPayment,
	)

	return err
}

func ReadAllPlans(pool *pgxpool.Pool) ([]Plan, error) {
	query := `SELECT name, cost_per_month, comment, allowed_ct_instances, 
		allowed_sign_instances, max_ct_balance, displayable_name, is_sold,
		initial_payment
		FROM plans`
	rows, err := pool.Query(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// scan the rows into the Plan structs
	var plans []Plan
	for rows.Next() {
		var p Plan
		err := rows.Scan(&p.Name, &p.CostPerMonth, &p.Comment, &p.AllowedCTInstances,
			&p.AllowedSignInstances, &p.MaxCTBalance, &p.DisplayableName, &p.IsSold,
			&p.InitialPayment)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}

	// check for errors from iterating over rows
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return plans, nil
}
