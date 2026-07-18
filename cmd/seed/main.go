// Command seed populates the database with realistic manufacturing data at scale:
// a multi-level BOM catalog, routings, production plans, and an inventory
// movement ledger large enough to make unoptimized queries measurably slow
// (see BENCHMARKS.md).
//
// Usage:
//
//	go run ./cmd/seed                          # default: 5k items, 200k movements
//	go run ./cmd/seed -items 50000 -movements 2000000 -plans 500
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rakshithyadhav/mrp-go/internal/config"
	"github.com/rakshithyadhav/mrp-go/internal/db"
)

var (
	nItems     = flag.Int("items", 5000, "total number of items (50% raw, 30% subassembly, 20% finished)")
	nMovements = flag.Int("movements", 200_000, "number of inventory movement rows")
	nPlans     = flag.Int("plans", 200, "number of production plans")
	seed       = flag.Uint64("seed", 42, "random seed for reproducibility")
)

func main() {
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("connect database", err)
	}
	defer pool.Close()

	rng := rand.New(rand.NewPCG(*seed, *seed))
	start := time.Now()

	if err := reset(ctx, pool); err != nil {
		fatal("reset tables", err)
	}
	warehouseIDs, resourceIDs, err := seedPlants(ctx, pool)
	if err != nil {
		fatal("seed plants", err)
	}
	cat, err := seedItems(ctx, pool, rng, *nItems)
	if err != nil {
		fatal("seed items", err)
	}
	stepCounts, err := seedRoutings(ctx, pool, rng, cat, resourceIDs)
	if err != nil {
		fatal("seed routings", err)
	}
	if err := seedBOMs(ctx, pool, rng, cat, stepCounts); err != nil {
		fatal("seed BOMs", err)
	}
	if err := seedPlans(ctx, pool, rng, cat, warehouseIDs, *nPlans); err != nil {
		fatal("seed plans", err)
	}
	if err := seedMovements(ctx, pool, rng, cat, warehouseIDs, *nMovements); err != nil {
		fatal("seed movements", err)
	}

	slog.Info("seed complete",
		"items", *nItems, "movements", *nMovements, "plans", *nPlans,
		"elapsed", time.Since(start).Round(time.Millisecond))
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}

func reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		TRUNCATE work_results, component_requirements, work_orders, purchase_requests,
		         production_orders, mrp_jobs, production_plans, inventory_movements,
		         bom_lines, bom_headers, routing_steps, routings,
		         items, resources, warehouses, holidays, plants
		RESTART IDENTITY CASCADE`)
	return err
}

func seedPlants(ctx context.Context, pool *pgxpool.Pool) (warehouseIDs, resourceIDs []int64, err error) {
	_, err = pool.Exec(ctx, `
		INSERT INTO plants (code, name) VALUES
		  ('P1', 'Main Plant'),
		  ('P2', 'Second Plant')`)
	if err != nil {
		return nil, nil, err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO warehouses (plant_id, code, name) VALUES
		  (1, 'WH1', 'Main Warehouse'),
		  (1, 'WH2', 'Line-side Store'),
		  (2, 'WH3', 'Second Plant Warehouse')`)
	if err != nil {
		return nil, nil, err
	}
	for i := 1; i <= 8; i++ {
		if _, err = pool.Exec(ctx,
			`INSERT INTO resources (plant_id, code, name) VALUES ($1, $2, $3)`,
			1+(i-1)%2, fmt.Sprintf("RES-%02d", i), fmt.Sprintf("Work Center %02d", i)); err != nil {
			return nil, nil, err
		}
	}
	// National holidays for both plants over a 2-year window.
	for _, d := range []string{
		"2026-01-01", "2026-01-12", "2026-02-11", "2026-02-23", "2026-03-20",
		"2026-04-29", "2026-05-04", "2026-05-05", "2026-07-20", "2026-08-11",
		"2026-09-21", "2026-10-12", "2026-11-03", "2026-11-23", "2026-12-31",
		"2027-01-01",
	} {
		if _, err = pool.Exec(ctx,
			`INSERT INTO holidays (plant_id, holiday) VALUES (1, $1::date), (2, $1::date)`, d); err != nil {
			return nil, nil, err
		}
	}
	return []int64{1, 2, 3}, []int64{1, 2, 3, 4, 5, 6, 7, 8}, nil
}

// catalog holds item ids grouped by role in the BOM hierarchy.
type catalog struct {
	raw  []int64 // buy items, tree leaves
	subs [][]int64 // make subassemblies by level: subs[0]=level1 ... subs[2]=level3
	fg   []int64 // finished goods, tree roots
}

func (c *catalog) makeItems() []int64 {
	out := append([]int64{}, c.fg...)
	for _, lvl := range c.subs {
		out = append(out, lvl...)
	}
	return out
}

func seedItems(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand, total int) (*catalog, error) {
	nRaw := total * 50 / 100
	nSub := total * 30 / 100
	nFG := total - nRaw - nSub

	rows := make([][]any, 0, total)
	add := func(code, name, itemType string, leadTime int, safetyStock float64) {
		lotRule, fixedLot := "lot_for_lot", any(nil)
		if rng.IntN(10) == 0 { // ~10% use fixed lot sizes
			lotRule, fixedLot = "fixed", any(float64(50*(1+rng.IntN(10))))
		}
		rows = append(rows, []any{code, name, itemType, "EA", leadTime, lotRule, fixedLot, safetyStock})
	}
	for i := 0; i < nRaw; i++ {
		add(fmt.Sprintf("RAW-%06d", i+1), fmt.Sprintf("Raw Material %06d", i+1), "buy",
			1+rng.IntN(14), float64(rng.IntN(500)))
	}
	for i := 0; i < nSub; i++ {
		add(fmt.Sprintf("SUB-%06d", i+1), fmt.Sprintf("Subassembly %06d", i+1), "make",
			1+rng.IntN(5), 0)
	}
	for i := 0; i < nFG; i++ {
		add(fmt.Sprintf("FG-%06d", i+1), fmt.Sprintf("Finished Product %06d", i+1), "make",
			1+rng.IntN(3), 0)
	}

	_, err := pool.CopyFrom(ctx, pgx.Identifier{"items"},
		[]string{"code", "name", "item_type", "uom", "lead_time_days", "lot_size_rule", "fixed_lot_size", "safety_stock"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return nil, err
	}

	// Read ids back grouped by code prefix; identity assignment follows insert order.
	cat := &catalog{subs: make([][]int64, 3)}
	res, err := pool.Query(ctx, `SELECT id, code FROM items ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	subIdx := 0
	for res.Next() {
		var id int64
		var code string
		if err := res.Scan(&id, &code); err != nil {
			return nil, err
		}
		switch code[:3] {
		case "RAW":
			cat.raw = append(cat.raw, id)
		case "SUB":
			cat.subs[subIdx%3] = append(cat.subs[subIdx%3], id) // spread subs across levels 1..3
			subIdx++
		case "FG-":
			cat.fg = append(cat.fg, id)
		}
	}
	return cat, res.Err()
}

// seedRoutings gives every make item a routing with 2-5 steps and returns the
// number of steps per item so BOM lines can reference a valid process_seq.
func seedRoutings(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand, cat *catalog, resourceIDs []int64) (map[int64]int, error) {
	makeItems := cat.makeItems()

	routingRows := make([][]any, 0, len(makeItems))
	for _, itemID := range makeItems {
		routingRows = append(routingRows, []any{itemID, "STD"})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"routings"},
		[]string{"item_id", "name"}, pgx.CopyFromRows(routingRows)); err != nil {
		return nil, err
	}

	routingByItem := map[int64]int64{}
	res, err := pool.Query(ctx, `SELECT id, item_id FROM routings`)
	if err != nil {
		return nil, err
	}
	for res.Next() {
		var id, itemID int64
		if err := res.Scan(&id, &itemID); err != nil {
			res.Close()
			return nil, err
		}
		routingByItem[itemID] = id
	}
	res.Close()
	if res.Err() != nil {
		return nil, res.Err()
	}

	stepNames := []string{"Cut", "Weld", "Machine", "Assemble", "Paint", "Inspect"}
	stepCounts := map[int64]int{}
	stepRows := make([][]any, 0, len(makeItems)*3)
	for _, itemID := range makeItems {
		n := 2 + rng.IntN(4) // 2-5 steps
		stepCounts[itemID] = n
		for s := 1; s <= n; s++ {
			stepRows = append(stepRows, []any{
				routingByItem[itemID],
				s * 10,
				stepNames[rng.IntN(len(stepNames))],
				resourceIDs[rng.IntN(len(resourceIDs))],
				float64(rng.IntN(4)) * 0.5,            // setup: 0-1.5h
				0.005 + rng.Float64()*0.05,            // 0.005-0.055 h/unit
			})
		}
	}
	_, err = pool.CopyFrom(ctx, pgx.Identifier{"routing_steps"},
		[]string{"routing_id", "seq", "name", "resource_id", "setup_hours", "hours_per_unit"},
		pgx.CopyFromRows(stepRows))
	return stepCounts, err
}

// seedBOMs builds a strictly layered tree (FG -> sub L3 -> L2 -> L1 -> raw) so
// the structure is guaranteed acyclic and up to 5 levels deep.
func seedBOMs(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand, cat *catalog, stepCounts map[int64]int) error {
	makeItems := cat.makeItems()

	headerRows := make([][]any, 0, len(makeItems))
	for _, itemID := range makeItems {
		headerRows = append(headerRows, []any{itemID, "STD"})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"bom_headers"},
		[]string{"item_id", "name"}, pgx.CopyFromRows(headerRows)); err != nil {
		return err
	}

	headerByItem := map[int64]int64{}
	res, err := pool.Query(ctx, `SELECT id, item_id FROM bom_headers`)
	if err != nil {
		return err
	}
	for res.Next() {
		var id, itemID int64
		if err := res.Scan(&id, &itemID); err != nil {
			res.Close()
			return err
		}
		headerByItem[itemID] = id
	}
	res.Close()
	if res.Err() != nil {
		return res.Err()
	}

	pick := func(pool []int64) int64 { return pool[rng.IntN(len(pool))] }
	lineRows := [][]any{}
	addLine := func(parent int64, child int64) {
		seq := (1 + rng.IntN(stepCounts[parent])) * 10 // consumed at a real routing step
		lineRows = append(lineRows, []any{headerByItem[parent], child, float64(1 + rng.IntN(10)), seq, 0.0})
	}

	for lvl, subs := range cat.subs {
		for _, subID := range subs {
			for i, n := 0, 2+rng.IntN(4); i < n; i++ { // 2-5 raw children
				addLine(subID, pick(cat.raw))
			}
			if lvl > 0 && len(cat.subs[lvl-1]) > 0 {
				for i, n := 0, 1+rng.IntN(2); i < n; i++ { // 1-2 lower-level subassemblies
					addLine(subID, pick(cat.subs[lvl-1]))
				}
			}
		}
	}
	for _, fgID := range cat.fg {
		deepest := cat.subs[2]
		if len(deepest) == 0 {
			deepest = cat.subs[0]
		}
		for i, n := 0, 2+rng.IntN(3); i < n; i++ { // 2-4 subassemblies
			addLine(fgID, pick(deepest))
		}
		for i, n := 0, 1+rng.IntN(3); i < n; i++ { // 1-3 direct raws
			addLine(fgID, pick(cat.raw))
		}
	}

	_, err = pool.CopyFrom(ctx, pgx.Identifier{"bom_lines"},
		[]string{"bom_header_id", "child_item_id", "qty_per", "process_seq", "scrap_pct"},
		pgx.CopyFromRows(lineRows))
	if err == nil {
		slog.Info("seeded BOMs", "headers", len(headerRows), "lines", len(lineRows))
	}
	return err
}

func seedPlans(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand, cat *catalog, warehouseIDs []int64, n int) error {
	rows := make([][]any, 0, n)
	today := time.Now()
	for i := 0; i < n; i++ {
		rows = append(rows, []any{
			fmt.Sprintf("PP-%06d", i+1),
			cat.fg[rng.IntN(len(cat.fg))],
			float64(10 + rng.IntN(490)),
			today.AddDate(0, 0, 7+rng.IntN(53)).Format("2006-01-02"),
			warehouseIDs[rng.IntN(len(warehouseIDs))],
			"draft",
		})
	}
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"production_plans"},
		[]string{"code", "item_id", "qty", "due_date", "warehouse_id", "status"},
		pgx.CopyFromRows(rows))
	return err
}

// seedMovements writes the append-only ledger in batches. Receipts outnumber
// issues so most items end with positive on-hand stock.
func seedMovements(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand, cat *catalog, warehouseIDs []int64, n int) error {
	itemPool := append(append([]int64{}, cat.raw...), cat.subs[0]...)
	itemPool = append(itemPool, cat.subs[1]...)
	itemPool = append(itemPool, cat.subs[2]...)

	const batchSize = 50_000
	now := time.Now()
	written := 0
	for written < n {
		size := min(batchSize, n-written)
		rows := make([][]any, 0, size)
		for i := 0; i < size; i++ {
			var qty float64
			var mtype string
			if rng.IntN(10) < 6 {
				qty, mtype = float64(10+rng.IntN(990)), "receipt"
			} else {
				qty, mtype = -float64(1+rng.IntN(400)), "issue"
			}
			rows = append(rows, []any{
				itemPool[rng.IntN(len(itemPool))],
				warehouseIDs[rng.IntN(len(warehouseIDs))],
				qty,
				mtype,
				now.Add(-time.Duration(rng.IntN(730*24)) * time.Hour),
			})
		}
		if _, err := pool.CopyFrom(ctx, pgx.Identifier{"inventory_movements"},
			[]string{"item_id", "warehouse_id", "qty", "movement_type", "moved_at"},
			pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		written += size
		slog.Info("seeding movements", "written", written, "total", n)
	}
	return nil
}
