package mrp

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

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
		tx: tx,
		planID: plan.id,
		dueDate: plan.dueDate,
		path: map[int64]bool{},
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

	if err := ex.schedule(ctx); err != nil {
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