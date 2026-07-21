// Command benchmrp measures a bulk MRP run: exploding every draft production
// plan currently in the database, synchronously, one after another — the
// naive baseline for BENCHMARKS.md §1. A single plan's explosion is too fast
// (milliseconds) to honestly represent "minutes" of processing delay; a bulk
// run across many plans (matching how the real UMProcess system's bulk MRP
// job works) is the workload that actually produces that number.
//
// Usage: reseed at benchmark scale, then run this against the fresh drafts.
//
//	go run ./cmd/seed -items 50000 -movements 2000000 -plans 500
//	go run ./cmd/benchmrp
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/rakshithyadhav/mrp-go/internal/config"
	"github.com/rakshithyadhav/mrp-go/internal/db"
	"github.com/rakshithyadhav/mrp-go/internal/mrp"
)

func main() {
	mode := flag.String("mode", "naive", "explosion mode: naive | optimized")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `SELECT id FROM production_plans WHERE status = 'draft' ORDER BY id`)
	if err != nil {
		slog.Error("query draft plans", "err", err)
		os.Exit(1)
	}
	var planIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			slog.Error("scan", "err", err)
			os.Exit(1)
		}
		planIDs = append(planIDs, id)
	}
	rows.Close()

	if len(planIDs) == 0 {
		slog.Error("no draft plans found - reseed first (see file header for command)")
		os.Exit(1)
	}

	svc := mrp.New(pool)
	explode := svc.Explode
	if *mode == "optimized" {
		explode = svc.ExplodeOptimized
	}

	var succeeded, failed int
	start := time.Now()

	for _, id := range planIDs {
		if _, err := explode(ctx, id); err != nil {
			failed++
			slog.Warn("explode failed", "plan_id", id, "err", err)
			continue
		}
		succeeded++
	}

	elapsed := time.Since(start)
	fmt.Printf("%s bulk MRP run: %d draft plans (%d succeeded, %d failed)\n", *mode, len(planIDs), succeeded, failed)
	fmt.Printf("total: %s | avg/plan: %s\n", elapsed.Round(time.Millisecond), (elapsed / time.Duration(len(planIDs))).Round(time.Millisecond))
}
