package mrp

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// querier is the read-only subset of pgx.Tx that loadCalendar needs. Narrowing it means the
// calendar can be built from anything that can run a query, not only a transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type scheduledProductionOrder struct {
	id         int64
	consumedBy *int64 // the parent work order that consumes this order's output (FR-5.3)
	due        time.Time
	start      time.Time
}

type scheduledWorkOrder struct {
	id       int64
	orderID  int64
	seq      int
	duration time.Duration
	start    time.Time
	end      time.Time
}

// scheduler walks one plan's pegging tree backward from the plan's due date.
//
// It runs as a separate pass after explosion and before netting, inside the same
// transaction (FR-3.5). Explosion has to exist first — the walk needs the work order rows
// and the consuming links — and netting has to come after, because a purchase request's
// need_by is a scheduling output (FR-5.4).
type scheduler struct {
	tx      pgx.Tx
	planID  int64
	planDue time.Time
	cal     *calendar

	rootProductionOrderID         int64
	productionOrdersByID          map[int64]*scheduledProductionOrder
	workOrdersByProductionOrderID map[int64][]*scheduledWorkOrder // ascending seq
	childOrderIDsByWorkOrderID    map[int64][]int64               // work order -> child order ids
	buyItemIDsByWorkOrderID       map[int64][]int64               // work order -> buy item ids

	earliestNeedByItemID map[int64]time.Time // FR-5.4, plan-scoped, keyed by item
	scheduledOrderIDs    map[int64]bool

	// Both summed while loading work orders. totalWork is the only input to the calendar's
	// horizon, which is why loadCalendar has to run after every duration has been read;
	// workOrderCount sizes the flush arrays exactly instead of guessing.
	totalWork      time.Duration
	workOrderCount int
}

const prodOrderQuery = `
SELECT id, consuming_work_order_id
FROM production_orders
WHERE plan_id = $1
`

// Duration lives on routing_steps, not on work_orders, so it is joined back through the
// order's item and the step's seq. FR-3.2 guarantees work_orders.seq == routing_steps.seq,
// and matching on both routing_id and seq is what stops every step pairing with every
// work order.
const workOrderQuery = `
SELECT w.id, w.production_order_id, w.seq, s.setup_hours + s.hours_per_unit * w.qty
FROM work_orders w
JOIN production_orders o ON o.id = w.production_order_id
JOIN routings r ON r.item_id = o.item_id AND r.is_active
JOIN routing_steps s ON s.routing_id = r.id AND s.seq = w.seq
WHERE o.plan_id = $1
ORDER BY w.production_order_id, w.seq
`

// production_orders appears here only to reach plan_id; nothing is selected from it.
// items must join on c.item_id (the component), not o.item_id (the item being made) —
// every production order's own item is a make item, so the latter returns nothing.
const buyRequirementsQuery = `
SELECT c.work_order_id, c.item_id
FROM component_requirements c
JOIN work_orders w ON w.id = c.work_order_id
JOIN production_orders o ON o.id = w.production_order_id
JOIN items i ON i.id = c.item_id
WHERE o.plan_id = $1 AND i.item_type = 'buy'
`

// One statement per table instead of one per row: the three parallel arrays are zipped by
// unnest into a joinable table. They must stay the same length — unnest pads a short array
// with NULLs rather than erroring.
const updateWorkOrder = `
UPDATE work_orders w
SET planned_start = v.ps, planned_end = v.pe
FROM unnest($1::bigint[],$2::timestamptz[],$3::timestamptz[]) AS v(id, ps, pe)
WHERE w.id = v.id
`

const updateProdOrder = `
UPDATE production_orders o
SET due_date = v.due, start_date = v.start
FROM unnest($1::bigint[],$2::timestamptz[],$3::timestamptz[]) AS v(id, due, start)
WHERE o.id = v.id
`

// schedule runs the backward pass and hands the purchase-request dates back to the exploder
// so netting can use them.
func (e *exploder) schedule(ctx context.Context) error {
	scheduler := &scheduler{tx: e.tx, planID: e.planID, planDue: e.dueDate}
	if err := scheduler.run(ctx); err != nil {
		return err
	}

	e.earliestNeedByItemID = scheduler.earliestNeedByItemID
	return nil
}

// needByFor gives a purchase request its date: the start of the earliest work order in the
// plan that requires the item (FR-5.4). Floored to a date because buyers work in days, and
// floored DOWN because rounding up would ask for delivery after the step already started.
// Falls back to the plan due date only if scheduling never saw the item.
func (e *exploder) needByFor(itemID int64) time.Time {
	needBy, ok := e.earliestNeedByItemID[itemID]
	if !ok {
		return e.dueDate
	}

	return time.Date(needBy.Year(), needBy.Month(), needBy.Day(), 0, 0, 0, 0, needBy.Location())
}

func (s *scheduler) run(ctx context.Context) error {
	if err := s.load(ctx); err != nil {
		return err
	}

	if s.rootProductionOrderID == 0 {
		return fmt.Errorf("plan %d has no root production order", s.planID)
	}

	// FR-5.1: the plan's due date lands on the root order, and within it on the LAST
	// routing step. Take it at the close of business on that day; snapBack pulls it to the
	// previous working moment if the plan is due on a holiday or a weekend.
	due, err := s.cal.snapBack(endOfDay(s.planDue, s.cal))
	if err != nil {
		return err
	}

	if err := s.scheduleOrder(s.rootProductionOrderID, due); err != nil {
		return err
	}

	// Invariant: every order in the tree was reached. An order left unvisited would keep
	// the placeholder date explosion stamped on it, which is the silent-wrong-answer case.
	if len(s.scheduledOrderIDs) != len(s.productionOrdersByID) {
		for id := range s.productionOrdersByID {
			if !s.scheduledOrderIDs[id] {
				return fmt.Errorf("production order %d was never reached by scheduling "+
					"(no consuming work order, or its parent has no routing)", id)
			}
		}
	}

	return s.flush(ctx)
}

// scheduleOrder sets an order's dates and recurses into the child orders each of its steps
// consumes. Steps are walked tail first; cursor carries each step's start down to its
// predecessor's end, which is the precedence rule of FR-5.2.
func (s *scheduler) scheduleOrder(orderID int64, due time.Time) error {
	order, ok := s.productionOrdersByID[orderID]
	if !ok {
		return fmt.Errorf("production order %d not in plan %d", orderID, s.planID)
	}
	if s.scheduledOrderIDs[orderID] {
		return fmt.Errorf("production order %d visited twice; pegging tree is not a tree", orderID)
	}
	s.scheduledOrderIDs[orderID] = true
	order.due = due
	cursor := due

	// Tail first: the order's due date belongs to its LAST step, and each step's start
	// becomes its predecessor's end. cursor carries that chain (FR-5.2).
	workOrders := s.workOrdersByProductionOrderID[orderID]
	for i := len(workOrders) - 1; i >= 0; i-- {
		workOrder := workOrders[i]

		end, err := s.cal.snapBack(cursor)
		if err != nil {
			return fmt.Errorf("work order %d: %w", workOrder.id, err)
		}
		start, err := s.cal.minusWorkingDuration(end, workOrder.duration)
		if err != nil {
			return fmt.Errorf("work order %d: %w", workOrder.id, err)
		}
		workOrder.end, workOrder.start = end, start

		// FR-5.3: a step consumes zero, one or many child orders. Each is due when THIS
		// step starts, not when the order's first step starts.
		for _, childID := range s.childOrderIDsByWorkOrderID[workOrder.id] {
			if err := s.scheduleOrder(childID, start); err != nil {
				return err
			}
		}

		// FR-5.4: running minimum across every work order in the plan needing the item.
		for _, itemID := range s.buyItemIDsByWorkOrderID[workOrder.id] {
			if cur, ok := s.earliestNeedByItemID[itemID]; !ok || start.Before(cur) {
				s.earliestNeedByItemID[itemID] = start
			}
		}

		cursor = start
	}

	order.start = cursor
	return nil
}

func (s *scheduler) load(ctx context.Context) error {
	s.productionOrdersByID = map[int64]*scheduledProductionOrder{}
	s.workOrdersByProductionOrderID = map[int64][]*scheduledWorkOrder{} // ascending seq
	s.childOrderIDsByWorkOrderID = map[int64][]int64{}                  // work order -> child order ids
	s.buyItemIDsByWorkOrderID = map[int64][]int64{}                     // work order -> buy item ids
	s.earliestNeedByItemID = map[int64]time.Time{}                      // FR-5.4, plan-scoped, keyed by item
	s.scheduledOrderIDs = map[int64]bool{}

	if err := s.loadProductionOrders(ctx); err != nil {
		return err
	}

	if err := s.loadWorkOrders(ctx); err != nil {
		return err
	}

	if err := s.loadBuyRequirements(ctx); err != nil {
		return err
	}

	// Last: the horizon argument is s.totalWork, which only exists once every work order
	// duration above has been read.
	cal, err := loadCalendar(ctx, s.tx, s.planID, s.planDue, s.totalWork)

	if err != nil {
		return err
	}
	s.cal = cal

	return nil
}

func (s *scheduler) loadBuyRequirements(ctx context.Context) error {
	rows, err := s.tx.Query(ctx, buyRequirementsQuery, s.planID)
	if err != nil {
		return err
	}

	for rows.Next() {
		var woId, itemId int64
		if err := rows.Scan(&woId, &itemId); err != nil {
			rows.Close()
			return err
		}

		s.buyItemIDsByWorkOrderID[woId] = append(s.buyItemIDsByWorkOrderID[woId], itemId)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// flush is the only place the walk touches the database. Everything before it is in-memory
// arithmetic, so a failure anywhere in the tree leaves no partial dates behind.
//
// Two statements total, one per table, not one per row.
func (s *scheduler) flush(ctx context.Context) error {
	// update work orders
	workOrderIds := make([]int64, 0, s.workOrderCount)
	woStarts := make([]time.Time, 0, s.workOrderCount)
	woEnds := make([]time.Time, 0, s.workOrderCount)

	// All three appends stay in one loop body: unnest pads a short array with NULLs instead
	// of erroring, so arrays drifting out of step would silently blank a real column.
	for _, workOrders := range s.workOrdersByProductionOrderID {
		for _, workOrder := range workOrders {
			workOrderIds = append(workOrderIds, workOrder.id)
			woStarts = append(woStarts, workOrder.start)
			woEnds = append(woEnds, workOrder.end)
		}
	}
	if len(workOrderIds) > 0 {
		if _, err := s.tx.Exec(ctx, updateWorkOrder, workOrderIds, woStarts, woEnds); err != nil {
			return fmt.Errorf("update work orders: %w", err)
		}
	}

	// update production orders
	prdOrderIds := make([]int64, 0, len(s.productionOrdersByID))
	prdDues := make([]time.Time, 0, len(s.productionOrdersByID))
	prdStarts := make([]time.Time, 0, len(s.productionOrdersByID))

	// Map iteration order is random in Go, and that is fine here: every row carries its own
	// id, so the UPDATE lands identically whatever order the arrays are built in.
	for _, order := range s.productionOrdersByID {
		prdOrderIds = append(prdOrderIds, order.id)
		prdDues = append(prdDues, order.due)
		prdStarts = append(prdStarts, order.start)
	}

	if len(prdOrderIds) > 0 {
		if _, err := s.tx.Exec(ctx, updateProdOrder, prdOrderIds, prdDues, prdStarts); err != nil {
			return fmt.Errorf("update production orders: %w", err)
		}
	}

	return nil
}

// endOfDay puts the plan's due DATE at the plant's closing time, since a plan due "on
// day 10" means by the end of day 10.
func endOfDay(due time.Time, cal *calendar) time.Time {
	// gets the due date from midnight.
	midNightDue := truncateDay(due)

	// Every interval shares the same daily window, so any of them gives the closing time.
	randomInterval := cal.intervals[0]
	closingDifference := randomInterval.end.Sub(truncateDay(randomInterval.end))
	return midNightDue.Add(closingDifference)
}

func (s *scheduler) loadProductionOrders(ctx context.Context) error {
	rows, err := s.tx.Query(ctx, prodOrderQuery, s.planID)
	if err != nil {
		return err
	}

	for rows.Next() {
		var prodOrder scheduledProductionOrder
		if err := rows.Scan(&prodOrder.id, &prodOrder.consumedBy); err != nil {
			rows.Close()
			return err
		}

		s.productionOrdersByID[prodOrder.id] = &prodOrder
		if prodOrder.consumedBy == nil {
			s.rootProductionOrderID = prodOrder.id
		} else {
			s.childOrderIDsByWorkOrderID[*prodOrder.consumedBy] = append(s.childOrderIDsByWorkOrderID[*prodOrder.consumedBy], prodOrder.id)
		}
	}

	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func (s *scheduler) loadWorkOrders(ctx context.Context) error {
	rows, err := s.tx.Query(ctx, workOrderQuery, s.planID)
	if err != nil {
		return err
	}

	for rows.Next() {
		var workOrder scheduledWorkOrder
		var hours float64
		if err := rows.Scan(&workOrder.id, &workOrder.orderID, &workOrder.seq, &hours); err != nil {
			rows.Close()
			return err
		}

		// time.Duration counts nanoseconds, so multiply in float space before converting —
		// time.Duration(hours) * time.Hour would truncate 2.5h to 2h.
		workOrder.duration = time.Duration(hours * float64(time.Hour))
		s.totalWork += workOrder.duration
		s.workOrderCount++
		s.workOrdersByProductionOrderID[workOrder.orderID] = append(s.workOrdersByProductionOrderID[workOrder.orderID], &workOrder)
	}

	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	return nil
}
