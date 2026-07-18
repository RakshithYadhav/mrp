package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rakshithyadhav/mrp-go/internal/domain"
)

func ListPlans(ctx context.Context, pool *pgxpool.Pool, limit, offset int) ([]domain.ProductionPlan, error) {
	rows, err := pool.Query(ctx, `
		SELECT p.id, p.code, p.item_id, i.code, i.name, p.qty, p.due_date, p.warehouse_id, p.status, p.created_at
		FROM production_plans p
		JOIN items i ON i.id = p.item_id
		ORDER BY p.due_date, p.id
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	plans := []domain.ProductionPlan{}
	for rows.Next() {
		var p domain.ProductionPlan
		if err := rows.Scan(&p.ID, &p.Code, &p.ItemID, &p.ItemCode, &p.ItemName,
			&p.Qty, &p.DueDate, &p.WarehouseID, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

type CreatePlanInput struct {
	ItemCode    string
	Qty         float64
	DueDate     string // YYYY-MM-DD
	WarehouseID int64  // 0 = default warehouse
}

func CreatePlan(ctx context.Context, pool *pgxpool.Pool, in CreatePlanInput) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var itemID int64
	var itemType string
	err = tx.QueryRow(ctx,
		`SELECT id, item_type FROM items WHERE code = $1`, in.ItemCode).Scan(&itemID, &itemType)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("item %q not found", in.ItemCode)
	}
	if err != nil {
		return 0, err
	}
	if itemType != "make" {
		return 0, fmt.Errorf("item %q is a buy item; production plans require a make item", in.ItemCode)
	}

	warehouseID := in.WarehouseID
	if warehouseID == 0 {
		if err := tx.QueryRow(ctx, `SELECT id FROM warehouses ORDER BY id LIMIT 1`).Scan(&warehouseID); err != nil {
			return 0, fmt.Errorf("no warehouse configured: %w", err)
		}
	}

	var planID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO production_plans (item_id, qty, due_date, warehouse_id, status)
		VALUES ($1, $2, $3::date, $4, 'draft')
		RETURNING id`, itemID, in.Qty, in.DueDate, warehouseID).Scan(&planID)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE production_plans SET code = 'PP-' || lpad($1::text, 6, '0') WHERE id = $1`, planID); err != nil {
		return 0, err
	}
	return planID, tx.Commit(ctx)
}
