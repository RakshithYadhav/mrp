# mrp-go — Production Scheduling & MRP Engine

A manufacturing production-management backend in **Go + PostgreSQL**: multi-level BOM
explosion (MRP), backward scheduling against a working-day calendar, an append-only
inventory ledger, and a live dashboard. Modeled on a commercial seiban-style
production management system for Japanese manufacturers.

## What it does

1. **Masters** — items (make/buy), multi-level BOMs, routings (process steps with
   work centers and hours-per-unit), plants with holiday calendars, warehouses.
2. **Planning (MRP)** — a production plan (item, qty, due date) is *exploded*
   through the BOM tree: manufacturing orders per make-item, work orders per routing
   step, component requirements pinned to the consuming step, purchase requests for
   buy-items. Requirements are *netted* against on-hand stock and safety stock, then
   *backward scheduled* from the due date, skipping holidays.
3. **Execution** — work results reported against work orders backflush component
   consumption into an append-only inventory ledger; order status rolls up.
4. **Visibility** — stock-on-hand and time-phased stock projection endpoints; MRP
   runs are async jobs with live progress (SSE).

## Architecture

```
cmd/
  api/       HTTP server (chi), graceful shutdown
  migrate/   embedded SQL migrations runner
  seed/      scale data generator (50k items / 2M ledger rows)
internal/
  config/    env config
  db/        pgx pool, migrations
  domain/    core types
  http/      router, middleware, handlers
  repo/      SQL data access
  (planned) mrp/       explosion, netting, lot sizing, scheduling
  (planned) jobs/      async worker pool, advisory-lock run guard
```

Layering follows handler → service → repository. On-hand stock is **derived** from
the ledger (`SUM(qty)`), never stored mutable — auditability and lot traceability
over write convenience; a snapshot projection is added as a measured optimization
(see [BENCHMARKS.md](BENCHMARKS.md)).

## Quickstart

```sh
docker compose up -d          # postgres 16 on host port 5433
go run ./cmd/migrate          # apply schema
go run ./cmd/seed             # 5k items, 200k movements (dev scale)
go run ./cmd/api              # api on :8090
```

Benchmark scale: `go run ./cmd/seed -items 50000 -movements 2000000 -plans 500`

Try it:

```sh
curl localhost:8090/api/items?q=FG-&limit=5
curl localhost:8090/api/plans
curl -X POST localhost:8090/api/plans -d '{"item_code":"FG-000001","qty":100,"due_date":"2026-08-15"}'
```

## Docs

- [`docs/FRD.md`](docs/FRD.md) — functional requirements: what the system must do, by
  requirement ID (FR-1 … FR-11), plus the non-functional targets this project measures
  itself against.
- [`docs/adr/`](docs/adr/) — architecture decision records: how each open design
  question in the FRD was actually resolved, and why.
- [`docs/concepts/`](docs/concepts/) — one file per day/unit: the rationale behind
  non-obvious code choices, plus a no-notes self-check for interview-defense practice.

## Roadmap

- [x] Day 1 — schema, migrations, scale seeder, masters API. Hand-recoded and verified
      end-to-end (config, db, migrate, repo, HTTP layer, `cmd/api`, `cmd/migrate` all
      rebuilt from scratch and reviewed; `cmd/seed` kept as originally scaffolded, by
      deliberate choice — see `docs/concepts/day-01-foundations.md`).
- [ ] Day 2 — MRP v1: synchronous explosion + netting + backward scheduling (deliberately naive, measured)
- [ ] Day 3 — MRP v2: async jobs, recursive CTE, bulk insert, bounded concurrency, SSE progress, advisory locks
- [ ] Day 4 — execution: work results, backflush, ledger; stock dashboard v1 → v2 (indexes + snapshot)
- [ ] Day 5 — Docker image, GitHub Actions CI/CD, cloud deploy, uptime monitoring
- [ ] Day 6 — React (TypeScript) dashboard: plans, run-MRP with live status, stock projection chart
