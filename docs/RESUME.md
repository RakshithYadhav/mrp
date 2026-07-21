# Resume Harvest — mrp-go

This project exists to substantiate specific resume claims. This file is the ledger:
which claims it targets, what evidence backs each, and the drafted bullets with their
interview dossiers. Rule: **a bullet without linked evidence doesn't get written.**

## The bullet formula (hard rule)

1. Past-tense leading verb ("Developed," "Optimized," "Implemented").
2. Exactly 3 capitalized keyword phrases ("Go, REST APIs, and Microservices").
3. How it was used — the concrete thing built or fixed.
4. Non-technical business reason — why it mattered, not just the engineering.
5. Quantified impact only if the number is real and measured. No number, no claim.
6. One sentence, 30–45 words.
7. Honest attribution — never claim outcomes not personally driven.

**Honesty boundary:** this is a personal project. Its bullets demonstrate *mechanisms*
(how to build near-real-time processing, how to cut query times) — business-outcome
numbers (revenue, org-level percentages, uptime SLAs) belong to real jobs, never here.

## Submitted resume bullets → mrp-go evidence map

The 8 bullets on the currently-submitted resume, labeled A–H. mrp-go's job is to make the
*mechanism* behind each technically discussable in depth. The business-outcome numbers in
them (35%, ¥20M, 30%, 25%, 99.9%) belong to the real job — mrp-go never claims those; it
arms the "how would you build that" conversation.

| # | Submitted bullet (abridged) | mrp-go mechanism | Evidence | Status |
|---|---|---|---|---|
| A | Architected **TypeScript, Node.js, PostgreSQL** for production scheduling (−35% planning effort) | Postgres side: shipped Day 1. TS side: Day 6 dashboard | schema, ADR-0001; Day 6 code | ✅ partial |
| B | Developed **Go, REST APIs, Microservices** — minutes → near real-time | Naive sync **bulk** MRP run (many plans, not one) → async worker + CTE + bulk insert, both measured at bulk scale. A single plan's naive explosion is only ~18ms (measured 2026-07-20, 23-header tree) — nowhere near "minutes"; the honest claim requires bulk-run scale, matching why the real UMProcess has a dedicated bulk-execution feature | BENCHMARKS §1 (bulk scale), FRD NFR-1, ADRs (traversal, jobs) | ⏳ Days 2–3 |
| C | Partnered with **Engineering Teams, Product Stakeholders, Business Users** — reliability, ¥20M products | Real-job story only — mrp-go deliberately N/A. Prep separately: batch error-event logging + Chatter alerting in the real system are the "reliability initiatives" | — | 🚫 not this project |
| D | Led **React, REST APIs, Cloud Services** — real-time visibility (−30% decision time) | Dashboard with live MRP status over SSE + stock projection chart | Day 6 code + demo | ⏳ Day 6 |
| E | Implemented **AWS, Docker, CI/CD Pipelines** — 30 min → <5 min deploys | Manual deploy documented + timed once, then Actions pipeline measured | BENCHMARKS §3 + Actions run history | ⏳ Day 5 |
| F | Designed **Azure Infrastructure, Containerized Services, Cloud-Native Applications** — 99.9% | Health-gated deploy, graceful shutdown, readiness probes, uptime monitor | FR-11 evidence + monitor history | ⏳ Day 5 |
| G | Developed **SQL Databases, Data Models, Backend Services** — information accuracy (−25% coordination) | 17-table schema, append-only ledger as single source of truth, FK/CHECK integrity | ✅ shipped — see B1 below | ✅ shipped |
| H | Optimized **Database Design, Query Performance, Application Logic** — 8s → <3s | Unindexed ledger SUM at 2M rows → covering index + snapshot, EXPLAIN before/after | BENCHMARKS §2 | ⏳ Day 4 |

## Drafted bullets

### B1 — data modeling (Day 1, shipped 2026-07-19)

> Designed PostgreSQL Schemas, Data Models, and REST APIs for a seiban-style manufacturing
> MRP system, modeling multi-level BOMs, routings, and an append-only inventory ledger to
> give production planners a single accurate source of truth for stock and plans.

*37 words. No quantified claim — nothing measured yet that belongs in a bullet.*

**Evidence:** `internal/db/migrations/0001_init.sql` (17-table schema),
`docs/adr/0001-*.md` (BOM header/lines decision), `docs/FRD.md` §7 (ledger + seiban
business rules), `docs/concepts/day-01-foundations.md`, commit history (hand-recoded).

**Interview dossier — hardest expected questions:**
1. Why is stock a `SUM` over an append-only ledger instead of a balance column? What did
   that trade off, and how do you plan to fix the read cost? *(FRD §7, Day 4 plan)*
2. Why two BOM tables (header + lines) instead of one flat edge table? *(ADR-0001)*
3. What does `process_seq` on a BOM line actually drive, and what breaks without it?
   *(FRD FR-3.3/FR-5.3)*
4. What did you deliberately leave out versus the real system you modeled this on, and
   why? *(FRD §7 — make-to-stock toggle, effectivity dates, co-products)*
5. Why `NUMERIC` for quantities but `float64` in Go — what's the precision trade-off?
   *(day-01-foundations.md §2)*

### (next: B2 near-real-time MRP — draft only after Day 3's second measurement exists, at bulk-run scale)

## Open gaps to resolve before drafting affected bullets

- **Bullet B's "Microservices" keyword.** Current architecture (`CLAUDE.md`) is a
  **modular monolith** — one Go binary, layered internally, async worker planned for
  Day 3 — not separately-deployable services over a network. Two ways to close this,
  undecided as of 2026-07-20: (a) split the Day 3 worker into its own deployable process
  talking to Postgres directly (genuinely a second service, modest lift since the job
  queue is already table-based), or (b) keep the monolith and be ready to explain the
  monolith-vs-microservices trade-off honestly in an interview instead of claiming the
  keyword literally. Decide by Day 3.

## Harvest log

- 2026-07-20 — file created; B1 drafted against shipped Day 1 evidence. B updated to
  require bulk-run-scale measurement (single-plan naive explosion measured at ~18ms is
  too fast to honestly support a "minutes" claim). Microservices gap logged as an open
  decision. All other targets pending their milestones.
