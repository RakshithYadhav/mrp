// Optimized (v2) tree walk. Kept side by side with Day 2's explode.go
// deliberately — same output (production_orders/work_orders/
// component_requirements/buyReqs), same insert helpers reused unchanged, the
// only difference is that every lookup here is a map read against data
// loaded once upfront (tree.go), not a query issued per node. This is what
// lets both versions be measured and compared, and eventually toggled from a
// UI, instead of one replacing the other.
package mrp

import (
	"context"
	"fmt"
)

// explodeOptimized loads the whole tree in one recursive CTE (loadTree), then
// batch-loads every item/BOM-header/routing-step it needs in a fixed handful
// of queries (not one per node), then walks the already-in-memory result.
func (e *exploder) explodeOptimized(ctx context.Context, root item, qty float64) error {
	edges, err := e.loadTree(ctx, root.id, qty)
	if err != nil {
		return err // ErrCycle surfaces here, before any writes (FR-3.4)
	}

	// Every distinct item id appearing anywhere - root, every parent, every
	// child - for one batched item lookup.
	idSet := map[int64]bool{root.id: true}
	for _, ed := range edges {
		idSet[ed.parentItemID] = true
		idSet[ed.childItemID] = true
	}
	allIDs := make([]int64, 0, len(idSet))
	for id := range idSet {
		allIDs = append(allIDs, id)
	}
	items, err := e.loadItemsBatch(ctx, allIDs)
	if err != nil {
		return err
	}

	// A production order is needed for root, plus every child that turns out
	// to be a make item - buy children never get one, they become buyReqs.
	orderIDSet := map[int64]bool{root.id: true}
	for _, ed := range edges {
		if items[ed.childItemID].itemType == "make" {
			orderIDSet[ed.childItemID] = true
		}
	}
	orderItemIDs := make([]int64, 0, len(orderIDSet))
	for id := range orderIDSet {
		orderItemIDs = append(orderItemIDs, id)
	}

	bomHeaders, err := e.loadBomHeadersBatch(ctx, orderItemIDs)
	if err != nil {
		return err
	}
	routingSteps, err := e.loadRoutingStepsBatch(ctx, orderItemIDs)
	if err != nil {
		return err
	}

	edgesByParent := make(map[int64][]treeEdge, len(orderItemIDs))
	for _, ed := range edges {
		edgesByParent[ed.parentItemID] = append(edgesByParent[ed.parentItemID], ed)
	}

	return e.walkNode(ctx, root.id, qty, 0, items, bomHeaders, routingSteps, edgesByParent)
}

// walkNode creates one item's production order, work orders, and component
// requirements, then recurses into make children - identical shape to Day
// 2's explode(), but every lookup here is a map read, not a query.
func (e *exploder) walkNode(
	ctx context.Context,
	itemID int64,
	qty float64,
	parentOrderID int64,
	items map[int64]item,
	bomHeaders map[int64]int64,
	routingSteps map[int64][]routingStep,
	edgesByParent map[int64][]treeEdge,
) error {
	bomHeaderID, hasBom := bomHeaders[itemID]

	orderID, err := e.insertProductionOrder(ctx, itemID, bomHeaderID, hasBom, qty, parentOrderID)
	if err != nil {
		return err
	}
	e.countProdOrders++

	steps := routingSteps[itemID]
	workOrderBySeq := make(map[int]int64, len(steps))
	var prevWO *int64
	for _, step := range steps {
		woID, err := e.insertWorkOrder(ctx, orderID, step, qty, prevWO)
		if err != nil {
			return err
		}
		workOrderBySeq[step.seq] = woID
		e.countWorkOrders++
		prevWO = &woID
	}

	if !hasBom {
		return nil
	}

	for _, ed := range edgesByParent[itemID] {
		workOrderID, ok := workOrderBySeq[ed.processSeq]
		if !ok {
			return fmt.Errorf(
				"BOM line child %d references process_seq %d with no matching routing step on item %d",
				ed.childItemID, ed.processSeq, itemID)
		}
		if err := e.insertComponentReq(ctx, workOrderID, ed.childItemID, ed.childQty); err != nil {
			return err
		}
		e.countComponentReqs++

		child := items[ed.childItemID]
		switch child.itemType {
		case "make":
			if err := e.walkNode(ctx, ed.childItemID, ed.childQty, orderID, items, bomHeaders, routingSteps, edgesByParent); err != nil {
				return err
			}
		case "buy":
			e.buyReqs[child.id] += ed.childQty
		}
	}
	return nil
}
