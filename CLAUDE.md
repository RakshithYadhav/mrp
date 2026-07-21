# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go + PostgreSQL production-management / MRP (Material Requirements Planning) backend,
built as a personal project to substantiate specific resume bullets (Go REST APIs,
SQL data modeling, query optimization, async job processing, CI/CD, cloud deploy) with
a project the author can defend in depth at a staff-level interview.x

The domain and data model are informed by a real commercial Salesforce production-management
package (seiban-style MRP for Japanese manufacturers) that the author works on professionally,
reverse-engineered from its Apex business logic — this repo is an independent reimplementation
in Go/Postgres, not a port of that code.

**Working method — read this before writing code.** This went through a few false starts
before landing (see git history of this file if curious); this is the settled version.

1. **Design decisions stay the author's, made collaboratively, before code exists.**
   `docs/FRD.md` §8 lists the open architecture questions (AD-1, AD-2, …); each gets an ADR
   in `docs/adr/`. Engage *while* the author is still deciding — lay out the real options
   with honest trade-offs, give a recommendation and say so plainly, push back if they're
   about to pick something that won't hold up under interview questioning. The decision
   only counts as theirs once they can state the why unprompted, not once it's written down.
2. **Claude writes the code.** Once a design decision is made, build it — same speed and
   directness as Day 1. Do not withhold implementation, package scaffolding, or wiring into
   `main.go`/router/handlers as some kind of exercise; that was tried and explicitly walked
   back by the author (they want to *understand and reproduce fast*, not originate from a
   blank page under time pressure they don't have this week).
3. **Teach the rationale as/after it's built — and persist it, never chat-only, every
   time, without being asked.** For every non-obvious choice in what just got written —
   why this package boundary, why this query shape, why this concurrency primitive — write
   it to `docs/concepts/<day-or-unit>.md` (one file per day or self-contained unit), not
   just into the conversation. Chat gets compacted/lost; these docs are how the reasoning
   survives and how the author reviews before an interview without needing to ask Claude to
   regenerate it. **This applies to every follow-up question in a thread too, not just the
   first explanation of a file** — the author has had to say "add that to notes" or "add
   all these questions and answers" repeatedly because follow-ups got answered in chat only
   and left unpersisted; that's the failure mode to stop, not a one-time reminder to
   accommodate. After answering any explanatory question during this project, the next
   action is writing it into the relevant `docs/concepts/` file — before waiting to see if
   the author asks for it. Explaining it once in chat and calling it done is an incomplete
   job — writing the file is part of finishing the teaching step, not an optional extra.
   End each concept doc with a short no-notes self-check (questions, no answers given)
   covering the choices just explained — that's the oral-defense material for later. Skip
   pure boilerplate (config loading, gitignore) not worth a doc — reserve these for pieces
   with real technique.
4. **The author reproduces it in a scratch copy, then it replaces Claude's version in the
   real repo.** After reading the concept doc (and using its self-check cold, before
   peeking back at the code), the author rebuilds the unit themselves quickly, in a scratch
   copy — not by editing the real repo directly, so there's still something to diff against.
   Then: a short review together (walk the diff, discuss anything that changed or is
   missing), and the author's version replaces Claude's in `internal/` — Claude's original
   was scaffolding to teach from, not the thing that ships. The repo's actual history should
   end up reflecting code the author wrote, not code Claude wrote and the author copied.
   Day 1 is the one exception on disk today (predates this rule) — treat FR-3 onward as
   subject to it by default.
5. **No Claude attribution in any commit in this repository, full stop — not just for
   recoded application code.** This is a resume-facing personal project; every commit
   message, including ones for docs Claude actually drafted (FRD, ADRs, concept notes,
   this file), must NOT include a `Co-Authored-By: Claude` trailer or any other mention of
   Claude/Anthropic. Do not add this by default the way the standard git-commit habit
   would. If unsure whether a change qualifies, leave attribution out — the safe default
   here is silence, not disclosure. (One exception already exists on disk: the original
   "Day 1" commit predates this rule and does carry a Claude trailer — leave that historical
   commit as-is, do not rewrite published history to fix it, just don't repeat the mistake.)
6. **Performance-sensitive features still get built in two stages.** A naive v1 (synchronous,
   N+1, unindexed) is built and measured before it's optimized (async, set-based, indexed)
   and measured again — every number goes in `BENCHMARKS.md`, reproducible by command. Don't
   skip straight to the optimized version; don't optimize before there's a measurement.
7. **Resume harvest after every shipped milestone — `docs/RESUME.md` is a tracked
   deliverable.** That file maps the author's 8 submitted resume bullets (A–H) to the
   mrp-go evidence backing each, holds drafted bullets (built strictly to the formula
   written at the top of that file — 3 capitalized keyword phrases, 30–45 words, real
   measured numbers only, honest attribution), and an interview dossier per bullet (the
   hardest questions an interviewer could ask, answerable from this repo's own docs).
   When a milestone ships, update the map's status column and draft/refine bullets for
   what actually shipped — without being asked. Never let a bullet claim a number that
   isn't in `BENCHMARKS.md`, and never let a personal-project bullet read as a
   production/employer outcome.

## Commands

```sh
docker compose up -d                                    # Postgres 16, host port 5433 (5432 is taken by another local project — do not change back to 5432)
go run ./cmd/migrate                                     # apply embedded SQL migrations
go run ./cmd/seed                                        # dev scale: 5k items, 200k ledger movements
go run ./cmd/seed -items 50000 -movements 2000000 -plans 500   # benchmark scale, for BENCHMARKS.md numbers
go run ./cmd/api                                         # API on :8090 (8080 is taken by Jenkins on the dev machine — do not change back to 8080)

go build ./...
go vet ./...
go test ./...                                            # no test files yet as of Day 1
go test ./internal/mrp/... -run TestExplode -v            # pattern for running a single test once mrp package exists
```

`DATABASE_URL` and `PORT` env vars override the defaults in `internal/config/config.go`
(`postgres://mrp:mrp@localhost:5433/mrp` and `8090`).

Reset and reseed from scratch: `docker compose down -v && docker compose up -d`, then
re-run migrate and seed — `cmd/seed` truncates all tables itself, so re-running it alone
without a fresh container also works.

## Architecture

Layering is `handler → service → repo`, deliberately mirroring the Controller → Service →
Logic → Dao pattern of the source Salesforce system:
- `internal/http/handlers` — HTTP concerns only (decode, validate, respond); no SQL
- `internal/repo` — all SQL lives here, one file per aggregate (`items.go`, `plans.go`)
- `internal/mrp` (planned) — explosion, netting, lot sizing, backward scheduling — the domain
  core, framework-independent
- `internal/jobs` (planned) — async worker pool + advisory-lock run guard for MRP jobs
- `internal/domain` — plain structs shared across layers, no behavior

Migrations are a hand-rolled embedded-SQL runner (`internal/db/migrate.go`, uses `go:embed`
on `internal/db/migrations/*.sql`), not golang-migrate/goose — applies each numbered file in
one transaction and records it in a `schema_migrations` table. New migrations are new
numbered files (`000N_description.sql`); this project does not support down-migrations.

### Data model (`internal/db/migrations/0001_init.sql`)

The BOM is self-referencing: `bom_headers` (one per make item) → `bom_lines` (child item +
qty_per + `process_seq`). `process_seq` pins a component to the specific routing step that
consumes it — component due dates come from when that step starts, not from the order as a
whole. `routings` → `routing_steps` carry the per-step work center, setup time, and
hours-per-unit used for both scheduling and capacity.

`inventory_movements` is an **append-only ledger** — on-hand stock is always `SUM(qty)`,
never a mutable balance column. This is a deliberate design choice (auditability, lot
traceability, no write contention on a shared balance row) that trades off read performance;
the read-side cost is exactly what Day 4's indexing/snapshot-table optimization measures and
fixes. Do not add a mutable stock-balance column as a "simpler" alternative — that undoes the
point of the exercise.

`production_plans` → `production_orders` (self-referencing via `parent_order_id`, one tree
per plan — mirrors seiban/make-to-order pegging, not textbook cross-order netting) →
`work_orders` (linked via `prev_work_order_id`) → `component_requirements` /
`purchase_requests`. `mrp_jobs` tracks async MRP run status (queued/running/succeeded/failed)
for UI polling, independent of whether the current MRP implementation is actually async yet.

### Seeder (`cmd/seed/main.go`)

Builds BOM trees **by construction** (raw materials → subassembly levels 1–3 → finished
goods, each level only referencing the level below) rather than generating random edges and
checking for cycles — this guarantees an acyclic, bounded-depth (up to 5 levels) tree every
run. Uses `pgx.CopyFrom` (Postgres `COPY`) for all bulk inserts, not row-by-row `INSERT` — this
is intentional (fast, realistic seeding) and is not itself the "naive version" being measured
elsewhere; the naive-vs-optimized comparisons apply to the MRP engine and read queries, not to
seeding.

## Roadmap state

Day 1 (schema, migrations, seeder, masters CRUD) is done. See README.md's Roadmap section for
what's next — check it before assuming a feature (MRP explosion, async jobs, backflush, stock
dashboard, CI/CD, frontend) already exists.
