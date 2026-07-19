package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rakshithyadhav/mrp-go/internal/domain"
)

const planQuery = 
`
SELECT 
	p.id, 
	p.code, 
	p.item_id, 
	i.code, 
	i.name, 
	p.qty, 
	p.due_date, 
	p.warehouse_id, 
	p.status, 
	p.created_at
FROM 
	production_plans p
JOIN 
	items i ON i.id = p.item_id
ORDER BY p.due_date, p.id
LIMIT $1 
OFFSET $2
`

const itemQ =
`
SELECT
	id,
	item_type
FROM
	items
WHERE
	code = $1
`

const warehouseQuery = 
`
SELECT
	id
FROM
	warehouses
ORDER BY
	id
LIMIT 1
`
const productionPlanInsertQuery = 
`
INSERT INTO 
	production_plans (item_id, qty, due_date, warehouse_id, status)
VALUES ($1, $2, $3::date, $4, 'draft')
RETURNING id
`

func ListPlans(ctx context.Context, pool *pgxpool.Pool, limit, offset int) ([]domain.ProductionPlan, error) {
	rows, err := pool.Query(ctx, planQuery, limit, offset)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	plans := []domain.ProductionPlan{}
	for rows.Next() {
		var plan domain.ProductionPlan
		if err := rows.Scan(
			&plan.ID,
			&plan.Code,
			&plan.ItemID,
			&plan.ItemCode,
			&plan.ItemName,
			&plan.Qty,
			&plan.DueDate,
			&plan.WarehouseID,
			&plan.Status,
			&plan.CreatedAt,
		); err != nil {
			return nil, err
		}

		plans = append(plans, plan)
	}

	return plans, rows.Err()
}

type CreatePlanInput struct {
	ItemCode string
	Qty float64
	DueDate string
	WarehouseID int64
}

//Start a transaction, with defer tx.Rollback(ctx) immediately as a safety net.
// Look up the item by code — confirm it exists, and confirm it's a "make" item (fail with a clear message if not).
// Pick a warehouse — use the caller's choice, or fall back to "the first one" if none was given.
// Insert the new plan row (no code yet), getting its generated id back via RETURNING.
// Use that id to set the plan's code in a second statement ('PP-' || lpad(...)).
// Commit — only reached if every step above succeeded; if anything failed earlier, the deferred rollback already undid it all.
// Return the new plan's id.
// So: CreatePlan = validate a proposed plan against real data 
// (does this item exist, is it actually makeable, which warehouse) — 
// then create it and give it a proper code, as one atomic 
// unit that either fully succeeds or leaves no trace at all.

func CreatePlan(ctx context.Context, pool *pgxpool.Pool, input CreatePlanInput ) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var itemId int64
	var itemType string
	err = tx.QueryRow(ctx, itemQ, input.ItemCode).Scan(&itemId, &itemType)

	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("item %q not found", input.ItemCode)
	}

	if err != nil {
		return 0, err
	}

	if itemType != "make" {
		return 0, fmt.Errorf("item %q is a buy item; production plans require a make item", input.ItemCode)
	}

	warehouseID := input.WarehouseID
	if warehouseID == 0 {
		if err := tx.QueryRow(ctx, warehouseQuery).Scan(&warehouseID); err != nil {
			return 0, fmt.Errorf("no warehouse configured: %w", err)
		}
	}

	var productionPlanId int64
	err = tx.QueryRow(ctx, productionPlanInsertQuery, itemId, input.Qty, input.DueDate, warehouseID).Scan(&productionPlanId)
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx,
	`UPDATE production_plans SET code = 'PP-' || lpad($1::text, 6, '0') WHERE id = $1`, productionPlanId); err != nil {
		return 0, err
	}
	return productionPlanId, tx.Commit(ctx)
}