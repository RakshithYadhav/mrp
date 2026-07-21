package mrp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// queries
const bomHeaderQuery = `
SELECT 
	id 
FROM 
	bom_headers
WHERE 
	item_id = $1 AND is_active = true
ORDER BY id 
LIMIT 1
`

// end queries

// models

// end models

type exploder struct {
	tx      pgx.Tx
	planID  int64
	dueDate time.Time

	path    map[int64]bool
	buyReqs map[int64]float64

	countProdOrders    int
	countWorkOrders    int
	countComponentReqs int
}

type item struct {
	id           int64
	itemType     string
	safetyStock  float64
	lotSizeRule  string
	fixedLotSize *float64
}

type bomLine struct {
	childItemId int64
	qtyPer      float64
	processSeq  int
	scrapPct    float64
}

type routingStep struct {
	seq        int
	name       string
	resourceID *int64
}

func (e *exploder) explode(ctx context.Context, item item, qty float64, parentOrderId int64) error {
	if e.path[item.id] {
		return fmt.Errorf("%w : item %d", ErrCycle, item.id)
	}
	e.path[item.id] = true
	defer delete(e.path, item.id)

	// bom
	bomHeaderId, hasBom, err := e.bomHeaderFor(ctx, item.id)
	if err != nil {
		return err
	}

	// production orders.
	orderId, err := e.insertProductionOrder(ctx, item.id, bomHeaderId, hasBom, qty, parentOrderId)
	if err != nil {
		return err
	}
	e.countProdOrders++

	// process
	steps, err := e.routingSteps(ctx, item.id)
	if err != nil {
		return err
	}

	workOrderBySeq := make(map[int]int64, len(steps))
	var prevWO *int64

	for _, step := range steps {
		woID, err := e.insertWorkOrder(ctx, orderId, step, qty, prevWO)
		if err != nil {
			return err
		}
		workOrderBySeq[step.seq] = woID
		e.countWorkOrders++
		prevWO = &woID
	}

	if !hasBom {
		return nil // leaf make item with no components
	}

	lines, err := e.bomLines(ctx, bomHeaderId)

	if err != nil {
		return err
	}

	for _, line := range lines {
		childQty := qty * line.qtyPer
		if line.scrapPct > 0 {
			childQty = childQty / (1 - line.scrapPct/100)
		}

		workOrderId, ok := workOrderBySeq[line.processSeq]
		if !ok {
			return fmt.Errorf(
				"BOM line child %d references process_seq %d with no matching routing step on item %d",
				line.childItemId, line.processSeq, item.id)
		}
		if err := e.insertComponentReq(ctx, workOrderId, line.childItemId, childQty); err != nil {
			return err
		}
		e.countComponentReqs++

		child, err := e.loadItem(ctx, line.childItemId)
		if err != nil {
			return err
		}
		
		switch child.itemType {
		case "make":
			if err := e.explode(ctx, child, childQty, orderId); err != nil {
				return err
			}
		case "buy":
			e.buyReqs[child.id] += childQty
		}
	}
	return nil
}

func (e *exploder) loadItem(ctx context.Context, id int64) (item, error) {
	var it item
	err := e.tx.QueryRow(ctx, `
		SELECT id, item_type, safety_stock, lot_size_rule, fixed_lot_size
		FROM items WHERE id = $1`, id).
		Scan(&it.id, &it.itemType, &it.safetyStock, &it.lotSizeRule, &it.fixedLotSize)
	return it, err
}

func (e *exploder) bomLines(ctx context.Context, headerId int64) ([]bomLine, error) {
	rows, err := e.tx.Query(ctx, `
	SELECT child_item_id, qty_per, process_seq, scrap_pct
	FROM bom_lines WHERE bom_header_id = $1
	ORDER BY id`, headerId)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var bomLines []bomLine
	for rows.Next() {
		var bom bomLine
		err := rows.Scan(&bom.childItemId, &bom.qtyPer, &bom.processSeq, &bom.scrapPct)
		if err != nil {
			return nil, err
		}
		bomLines = append(bomLines, bom)
	}
	return bomLines, rows.Err()
}

func (e *exploder) bomHeaderFor(ctx context.Context, itemID int64) (int64, bool, error) {
	var headerID int64
	err := e.tx.QueryRow(ctx, bomHeaderQuery, itemID).Scan(&headerID)

	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, err
	}

	return headerID, true, nil
}

func (e *exploder) routingSteps(ctx context.Context, itemID int64) ([]routingStep, error) {
	rows, err := e.tx.Query(ctx,`
		SELECT s.seq, s.name, s.resource_id
		FROM routings r
		JOIN routing_steps s ON s.routing_id = r.id
		WHERE r.item_id = $1 AND r.is_active = true
		ORDER BY s.seq`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []routingStep
	for rows.Next() {
		var step routingStep
		if err := rows.Scan(&step.seq, &step.name, &step.resourceID); err != nil {
			return nil, err
		}
		out = append(out, step)
	}
	return out, rows.Err()
}

func (e *exploder) insertWorkOrder(ctx context.Context, orderId int64, step routingStep, qty float64, prevWO *int64) (int64, error) {
	var workOrderId int64
	err := e.tx.QueryRow(ctx, `
	INSERT INTO work_orders (production_order_id, seq, name, resource_id, qty, prev_work_order_id)
	VALUES ($1,$2,$3,$4,$5,$6)
	RETURNING id`,
	orderId, step.seq, step.name, step.resourceID, qty, prevWO).Scan(&workOrderId)
	return workOrderId, err
}

func (e *exploder) insertProductionOrder(
	ctx context.Context, 
	itemID int64, 
	bomHeaderId int64 ,
	hasBom bool, 
	qty float64, 
	parentOrderId int64) (int64, error) {

		var header any
		if hasBom {
			header = bomHeaderId
		}

		var parent any
		if parentOrderId != 0 {
			parent = parentOrderId
		}

		var prodOrderId int64
		err := e.tx.QueryRow(ctx,`INSERT INTO production_orders (plan_id, parent_order_id, item_id, bom_header_id, qty, due_date)
							VALUES ($1, $2, $3, $4, $5, $6)
							RETURNING id`,
							e.planID,
							parent,
							itemID,
							header,
							qty,
							e.dueDate).Scan(&prodOrderId)
		
		return prodOrderId, err
}

func (e *exploder) insertComponentReq(ctx context.Context, workOrderID, itemID int64, qty float64) error {
	_, err := e.tx.Exec(ctx, `
		INSERT INTO component_requirements (work_order_id, item_id, qty_required)
		VALUES ($1, $2, $3)`, workOrderID, itemID, qty)
	return err
}
