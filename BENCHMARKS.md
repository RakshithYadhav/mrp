# Benchmarks

Every optimization in this project is measured. Each row records the workload, the
naive implementation's numbers, the optimized implementation's numbers, and how to
reproduce both (git tags mark each stage).

Machine: (fill in: CPU, RAM, Windows version, Docker Desktop) — Postgres 16 in Docker on
localhost:5433. Note: numbers are from this dev machine over a localhost socket; a real
deployment paying 0.5–2ms network latency per round trip would show an even larger naive/
optimized gap, since the naive version's cost scales with round-trip count.
Dataset: seeded with `go run ./cmd/seed -items 50000 -movements 2000000 -plans 500`

## 1. MRP run: naive per-node N+1 → recursive CTE + batched loads

Measured 2026-07-21. Workload: explode **all 500 draft plans** in one bulk run,
synchronously, one after another (a single plan is milliseconds — too fast to represent the
"several minutes" of real processing delay; bulk scale is the honest workload, and mirrors
the real UMProcess system's dedicated bulk MRP execution feature). Dataset: `go run
./cmd/seed -items 50000 -movements 2000000 -plans 500` (deterministic `-seed 42`, so naive
and optimized ran on identical data). Output verified **byte-identical** between the two
paths on plans 10, 42, 137 before benchmarking — same production orders, work orders,
component requirements, purchase requests.

| | Naive (`Explode`) | Optimized (`ExplodeOptimized`) |
|---|---|---|
| 500 plans, total | **25m 55s** | **1m 32s** |
| Avg per plan | 3.11 s | 185 ms |
| Speedup | — | **~16.8× faster** |
| Tree traversal | one query per BOM node (N+1) | one recursive CTE, whole tree, 1 round trip |
| Item / BOM-header / routing loads | one query per node each | one batched `= ANY($1)` query each for the whole tree |
| On-hand netting | one `SUM` query per buy item (N+1) | one `SUM ... GROUP BY item_id` for all buy items |
| Row inserts | individual (unchanged in both) | individual (unchanged — a further optimization left on the table) |
| Execution | synchronous, request-blocking | synchronous (async worker is the next step, AD-3/4/5) |

Reproduce:
```sh
go run ./cmd/seed -items 50000 -movements 2000000 -plans 500
go run ./cmd/benchmrp -mode naive       # 25m55s
# reseed (the run flips plans draft -> planned):
go run ./cmd/seed -items 50000 -movements 2000000 -plans 500
go run ./cmd/benchmrp -mode optimized   # 1m32s
```

**Why the gap:** the naive version pays a network round trip per BOM node (and per buy item
in netting) — three separate N+1 patterns. Each round trip is milliseconds of mostly-waiting
even on localhost; at ~13k nodes across 500 plans that dominates everything. The optimized
version collapses each N+1 into a fixed handful of set-based queries per plan, so Postgres
does the traversal in-process (nanoseconds-to-microseconds per step) instead of Go paying
network latency per step. Row inserts are still individual in both — deliberately, so this
comparison isolates the *query-pattern* change; bulk insert is a further win still on the
table. The remaining ~185ms/plan is now dominated by those per-row inserts, which is exactly
what a future bulk-insert pass (and the async worker) would attack next.

## 2. Stock dashboard: unindexed ledger SUM → indexed + snapshot

| | v1 | v2 |
|---|---|---|
| Workload | on-hand for 50 items over 2M-row ledger | same |
| Wall time | _measure_ | _measure_ |
| Plan | seq scan (paste EXPLAIN ANALYZE) | index-only scan / snapshot join (paste EXPLAIN ANALYZE) |
| Change | — | covering index on (item_id, warehouse_id, moved_at) INCLUDE (qty); running-balance snapshot table |

## 3. Deployment: manual → CI/CD

| | manual (documented + timed once) | pipeline |
|---|---|---|
| Steps | build, scp, ssh, restart, migrate by hand | git push |
| Wall time | _measure_ | _measure from Actions run_ |

## Method notes

- Timings: `hyperfine` for CLI paths, `curl -w "%{time_total}"` x10 (median) for HTTP paths.
- Always run twice, report warm numbers; note cold vs warm cache.
- Keep the raw EXPLAIN ANALYZE output in `docs/explain/` — interviewers can read it.
