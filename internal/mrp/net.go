package mrp

import (
	"context"
	"math"
	"sort"
)

const sumQtyQuery = `
SELECT
	COALESCE(SUM(qty), 0) 
FROM
	inventory_movements
WHERE 
	item_id = $1
`

const insertPurchaceRequest = `
INSERT INTO
	purchase_requests (plan_id, item_id, qty, need_by)
VALUES
	($1, $2, $3, $4)
`

func (e *exploder) net(ctx context.Context) (int, error) {
	itemIDs := make([]int64, 0, len(e.buyReqs))
	for id := range e.buyReqs {
		itemIDs = append(itemIDs, id)
	}

	sort.Slice(itemIDs, func(i, j int) bool {
		return itemIDs[i] < itemIDs[j]
	})

	count := 0
	for _, itemID := range itemIDs {
		total := e.buyReqs[itemID]

		item, err := e.loadItem(ctx, itemID)
		if err != nil {
			return count, err
		}

		var onHand float64
		if err := e.tx.QueryRow(ctx, sumQtyQuery, itemID).Scan(&onHand); err != nil {
			return count, err
		}

		net := total - onHand + item.safetyStock
		if net <= 0 {
			continue
		}

		if item.lotSizeRule == "fixed" && item.fixedLotSize != nil && *item.fixedLotSize > 0 {
			net = math.Ceil(net / *item.fixedLotSize) * *item.fixedLotSize
		}

		if _, err := e.tx.Exec(ctx, insertPurchaceRequest,
			e.planID,
			itemID,
			net,
			e.dueDate); err != nil {
			return count, err
		}
		count++
	}
	return count, nil

}
