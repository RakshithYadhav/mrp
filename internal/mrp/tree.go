// Optimized (v2) BOM traversal: one recursive CTE loads the whole tree in a
// single round trip, replacing Day 2's per-node bomHeaderFor/bomLines calls
// (see ADR-0002, docs/concepts/day-03-mrp-optimized.md). Two more N+1 spots
// from the naive version are batched here too: routing steps (one query for
// every item in the tree, not one per node) and, in net.go, the on-hand
// lookup (one grouped query, not one per buy item).
package mrp

import "context"

// treeEdge is one BOM line reachable from the plan's root item, with its
// quantity already multiplied down from the root (child_qty), computed by
// the CTE itself rather than by the Go recursion that used to do this.
type treeEdge struct {
	parentItemID  int64
	childItemID   int64
	processSeq    int
	childQty      float64
	depth         int
	cycleDetected bool
}

const treeQuery = `
WITH RECURSIVE exploded AS (
	SELECT
		bh.item_id AS parent_item_id,
		bl.child_item_id,
		bl.process_seq,
		($2::numeric * bl.qty_per / (1 - bl.scrap_pct / 100.0)) AS child_qty,
		1 AS depth,
		ARRAY[bh.item_id] AS path,
		false AS cycle_detected
	FROM bom_headers bh
	JOIN bom_lines bl ON bl.bom_header_id = bh.id
	WHERE bh.item_id = $1 AND bh.is_active = true

	UNION ALL

	SELECT
		e.child_item_id AS parent_item_id,
		bl2.child_item_id,
		bl2.process_seq,
		(e.child_qty * bl2.qty_per / (1 - bl2.scrap_pct / 100.0)),
		e.depth + 1,
		e.path || e.child_item_id,
		(bl2.child_item_id = ANY(e.path))
	FROM exploded e
	JOIN bom_headers bh2 ON bh2.item_id = e.child_item_id AND bh2.is_active = true
	JOIN bom_lines bl2 ON bl2.bom_header_id = bh2.id
	WHERE NOT e.cycle_detected
	  AND e.depth < 50 -- absolute safety cap regardless of the flag logic
)
SELECT parent_item_id, child_item_id, process_seq, child_qty, depth, cycle_detected
FROM exploded
ORDER BY depth, parent_item_id
`

// loadTree runs the recursive CTE once for the whole tree under rootItemID,
// given the plan's root quantity. Returns ErrCycle immediately if any edge
// was flagged — the caller (mrp.go) checks this before any INSERT happens,
// satisfying FR-3.4 (reject before writes), not just silently truncating the
// cyclic branch the way an unflagged recursive CTE would.
//
// Cycle vs. diamond, same semantics as Day 2's Go path-set, expressed
// differently: `path` is per-row (per-occurrence), not global, so the same
// item reached via two independent sibling branches gets two rows with two
// different paths, neither containing the other — a legal diamond. A true
// cycle means the child equals something already in *this row's own* path.
func (e *exploder) loadTree(ctx context.Context, rootItemID int64, rootQty float64) ([]treeEdge, error) {
	rows, err := e.tx.Query(ctx, treeQuery, rootItemID, rootQty)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []treeEdge
	for rows.Next() {
		var ed treeEdge
		if err := rows.Scan(&ed.parentItemID, &ed.childItemID, &ed.processSeq,
			&ed.childQty, &ed.depth, &ed.cycleDetected); err != nil {
			return nil, err
		}
		edges = append(edges, ed)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, ed := range edges {
		if ed.cycleDetected {
			return nil, ErrCycle
		}
	}
	return edges, nil
}

// loadItemsBatch replaces one loadItem call per node with one query for
// every distinct item appearing anywhere in the tree (parents and children).
func (e *exploder) loadItemsBatch(ctx context.Context, itemIDs []int64) (map[int64]item, error) {
	rows, err := e.tx.Query(ctx, `
		SELECT id, item_type, safety_stock, lot_size_rule, fixed_lot_size
		FROM items WHERE id = ANY($1)`, itemIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]item, len(itemIDs))
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.itemType, &it.safetyStock, &it.lotSizeRule, &it.fixedLotSize); err != nil {
			return nil, err
		}
		out[it.id] = it
	}
	return out, rows.Err()
}

// loadBomHeadersBatch replaces the per-node bomHeaderFor call with one query
// for every item that will get a production order (root + every make item
// reached in the tree). Absent from the returned map means "no active BOM
// header" — a leaf make item, or (the edge case this must not miss) a root
// item that itself has no BOM lines at all, so it never appears as a
// parent_item_id in loadTree's output.
func (e *exploder) loadBomHeadersBatch(ctx context.Context, itemIDs []int64) (map[int64]int64, error) {
	rows, err := e.tx.Query(ctx, `
		SELECT item_id, id FROM bom_headers
		WHERE item_id = ANY($1) AND is_active = true`, itemIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]int64, len(itemIDs))
	for rows.Next() {
		var itemID, headerID int64
		if err := rows.Scan(&itemID, &headerID); err != nil {
			return nil, err
		}
		out[itemID] = headerID
	}
	return out, rows.Err()
}

// loadRoutingStepsBatch replaces one routingSteps call per node with one
// query for every distinct item that will get a production order (every
// make item in the tree, root included).
func (e *exploder) loadRoutingStepsBatch(ctx context.Context, itemIDs []int64) (map[int64][]routingStep, error) {
	rows, err := e.tx.Query(ctx, `
		SELECT r.item_id, s.seq, s.name, s.resource_id
		FROM routings r
		JOIN routing_steps s ON s.routing_id = r.id
		WHERE r.item_id = ANY($1) AND r.is_active = true
		ORDER BY r.item_id, s.seq`, itemIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]routingStep, len(itemIDs))
	for rows.Next() {
		var itemID int64
		var st routingStep
		if err := rows.Scan(&itemID, &st.seq, &st.name, &st.resourceID); err != nil {
			return nil, err
		}
		out[itemID] = append(out[itemID], st)
	}
	return out, rows.Err()
}
