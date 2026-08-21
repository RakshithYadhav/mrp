package mrp

import ("context"
	"fmt")


func (e *exploder) explodeOptimized(ctx context.Context, root item, qty float64) error {
	edges, err := e.loadTree(ctx, root.id, qty)
	if err != nil {
		return err
	}

	// this is a set which gets all unique item ids from the tree.
	idSet := map[int64]bool{root.id: true}
	for _, ed := range edges {
		idSet[ed.parentItemId] = true
		idSet[ed.childItemID] = true
	}

	// collect all the unique ids.
	allIDs := make([]int64, 0, len(idSet))
	for id := range idSet {
		allIDs = append(allIDs, id)
	}

	items, err := e.loadItemsBatch(ctx, allIDs)
	if err != nil {
		return err
	}

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
		edgesByParent[ed.parentItemId] = append(edgesByParent[ed.parentItemId], ed)
	}

	return e.walkNode(ctx, root.id, qty, 0, 0, items, bomHeaders, routingSteps, edgesByParent)
}

// consumingWorkOrderID is the parent routing step consuming this item (FR-5.3); zero at the root.
func (e *exploder) walkNode(
	ctx context.Context,
	itemID int64,
	qty float64,
	parentOrderID int64,
	consumingWorkOrderID int64,
	items map[int64]item,
	bomHeaders map[int64]int64,
	routingSteps map[int64][]routingStep,
	edgesByParent map[int64][]treeEdge,
) error {
	bomHeaderID, hasBom := bomHeaders[itemID]

	orderId, err := e.insertProductionOrder(ctx, itemID, bomHeaderID, hasBom, qty, parentOrderID, consumingWorkOrderID)
	if err != nil {
		return err
	}
	e.countProdOrders++

	steps := routingSteps[itemID]
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
			if err := e.walkNode(ctx, ed.childItemID, ed.childQty, orderId, workOrderID, items, bomHeaders, routingSteps, edgesByParent); err != nil {
				return err
			}
		case "buy":
			e.buyReqs[child.id] += ed.childQty
		}
	}
	return nil
}

