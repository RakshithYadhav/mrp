package mrp

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ExplodeOptimized is the v2 counterpart to Explode: identical contract
// (same guards, same atomicity, same Result), but the tree walk uses one
// recursive CTE + batched loads (explode_optimized.go, tree.go) and netting
// uses one grouped query (net_optimized.go). Kept as a separate entry point
// so both versions stay callable and measurable side by side — the naive
// Explode is untouched.
func (s *Service) ExplodeOptimized(ctx context.Context, planID int64) (Result, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)

	var plan planRow
	err = tx.QueryRow(ctx,
		`SELECT id, item_id, qty, due_date, status
		 FROM production_plans WHERE id = $1 FOR UPDATE`, planID).
		Scan(&plan.id, &plan.itemID, &plan.qty, &plan.dueDate, &plan.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrPlanNotFound
	}
	if err != nil {
		return Result{}, err
	}
	if plan.status != "draft" {
		return Result{}, ErrAlreadyExploded
	}

	ex := &exploder{
		tx:      tx,
		planID:  plan.id,
		dueDate: plan.dueDate,
		path:    map[int64]bool{},
		buyReqs: map[int64]float64{},
	}

	root, err := ex.loadItem(ctx, plan.itemID)
	if err != nil {
		return Result{}, err
	}
	if root.itemType != "make" {
		return Result{}, fmt.Errorf("%w: item %d", ErrNotMakeItem, plan.itemID)
	}

	if err := ex.explodeOptimized(ctx, root, plan.qty); err != nil {
		return Result{}, err
	}

	purchaseCount, err := ex.netOptimized(ctx)
	if err != nil {
		return Result{}, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE production_plans SET status = 'planned' WHERE id = $1`, planID); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}

	return Result{
		PlanID:           planID,
		ProductionOrders: ex.countProdOrders,
		WorkOrders:       ex.countWorkOrders,
		ComponentReqs:    ex.countComponentReqs,
		PurchaseRequests: purchaseCount,
	}, nil
}
