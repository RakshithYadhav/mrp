package mrp

import (
	"context"
	"math"
	"sort"
)

func (e *exploder) netOptimized(ctx context.Context) (int, error) {
	if len(e.buyReqs) == 0 {
		return 0, nil
	}

	itemIDs := make([]int64, 0, len(e.buyReqs))
	for id := range e.buyReqs {
		itemIDs = append(itemIDs, id)
	}

	// if we dont sort, we get different results and it wont be deterministic 
	// and hard to check the benchmarks.
	sort.Slice(itemIDs, func(i, j int) bool {
		return itemIDs[i] < itemIDs[j]
	})

	items, err := e.loadItemsBatch(ctx, itemIDs)
	if err != nil {
		return 0, err
	}

	// current stock
	onHand := make(map[int64]float64, len(itemIDs))
	rows, err := e.tx.Query(ctx, `
		SELECT item_id, COALESCE(SUM(qty),0)
		FROM inventory_movements
		WHERE item_id = ANY($1)
		GROUP BY item_id`, itemIDs)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var sum float64
		if err := rows.Scan(&id, &sum); err != nil {
			return 0, err
		}
		onHand[id] = sum
	}

	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0

	for _, itemID := range itemIDs {
		it := items[itemID]
		net := e.buyReqs[itemID] - onHand[itemID] + it.safetyStock
		if net <= 0 {
			continue
		}
		if it.lotSizeRule == "fixed" && it.fixedLotSize != nil && *it.fixedLotSize > 0 {
			net = math.Ceil(net / *it.fixedLotSize) * *it.fixedLotSize
		}
		if _, err := e.tx.Exec(ctx, insertPurchaceRequest, e.planID, itemID, net, e.needByFor(itemID)); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}