package mrp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

var (
	// ErrPlanNotFound is returned when no plan matches the given id.
	ErrPlanNotFound = errors.New("plan not found")
	// ErrAlreadyExploded is returned when a plan is not in 'draft' status.
	ErrAlreadyExploded = errors.New("plan is not in draft status; already exploded")
	// ErrCycle is returned when the BOM tree references an ancestor (FR-3.4).
	ErrCycle = errors.New("BOM contains a cycle")
	// ErrNotMakeItem is returned when a plan's item cannot be manufactured.
	ErrNotMakeItem = errors.New("plan item is not a make item")
)

type Result struct {
	PlanID           int64 `json:"plan_id"`
	ProductionOrders int   `json:"production_orders"`
	WorkOrders       int   `json:"work_orders"`
	ComponentReqs    int   `json:"component_requirements"`
	PurchaseRequests int   `json:"purchase_requests"`
}

type planRow struct {
	id      int64
	itemID  int64
	qty     float64
	dueDate time.Time
	status  string
}

func (s *Service) Explode(ctx context.Context, planID int64) (Result, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)

	var plan planRow
	err = tx.QueryRow(ctx,
		`SELECT id, item_id, qty, due_date, status
	FROM production_plans
	WHERE id = $1
	FOR UPDATE`, planID).Scan(&plan.id, &plan.itemID, &plan.qty, &plan.dueDate, &plan.status)

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

	if err := ex.explode(ctx, root, plan.qty, 0); err != nil {
		return Result{}, err
	}

	purchaseCount, err := ex.net(ctx)
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
