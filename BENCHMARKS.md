# Benchmarks

Every optimization in this project is measured. Each row records the workload, the
naive implementation's numbers, the optimized implementation's numbers, and how to
reproduce both (git tags mark each stage).

Machine: (fill in: CPU, RAM, Windows version, Docker Desktop)
Dataset: seeded with `go run ./cmd/seed -items 50000 -movements 2000000 -plans 500`

## 1. MRP run: synchronous + N+1 → async + set-based + concurrent

| | v1 (`v1-sync`) | v2 (`v2-async`) |
|---|---|---|
| Workload | explode 1 plan, 5-level BOM | same |
| Wall time | _measure_ | _measure_ |
| SQL round trips | _count via pg_stat_statements_ | _count_ |
| Approach | per-node SELECT, per-row INSERT, in request thread | recursive CTE tree load, bulk COPY inserts, errgroup over BOM branches, worker pool + SSE progress |

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
