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
| B | Developed **Go, REST APIs, Microservices** — minutes → near real-time | Naive sync bulk MRP run → recursive CTE + batched loads. **Measured 2026-07-21: 500 plans 25m55s → 1m32s (~16.8×), per plan 3.11s → 185ms.** Output verified identical between paths. "Microservices" keyword still unearned (modular monolith) — see B2 note | BENCHMARKS §1, `internal/mrp/tree.go`, ADR-0002, B2 script below | ✅ measured |
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

### B2 — near-real-time MRP (Days 2–3, measured 2026-07-21)

> Developed Go, REST APIs, and Backend Services for a manufacturing production-scheduling
> engine, cutting a bulk MRP planning run from ~26 minutes to under 2 minutes by replacing
> per-node database round trips with set-based recursive queries and batched lookups,
> enabling planners to re-plan in near real-time instead of waiting on an overnight job.

*44 words. Real measured number (25m55s → 1m32s, ~16.8×). Note: keyword phrases are "Go,
REST APIs, Backend Services" — NOT "Microservices," because this is honestly a modular
monolith (see the honesty boundary below). If the submitted resume says "Microservices,"
either close that gap (split the async worker into its own service, Day 3+) or be ready to
own the monolith choice out loud — do not claim the keyword the architecture doesn't earn.*

**Evidence:** `BENCHMARKS.md` §1 (both numbers, reproduction commands), `internal/mrp/`
(both paths kept side by side — `Explode` naive, `ExplodeOptimized` set-based),
`internal/mrp/tree.go` (the recursive CTE), `docs/adr/0002-naive-vs-cte.md`,
`docs/concepts/mrp-fundamentals.md` + `day-02-mrp-explosion.md`, `cmd/benchmrp`.

---

## Interview script for B2 — the near-real-time MRP story

### The 60-second opener (say this, then stop and let them steer)

"I work on a Salesforce-based manufacturing MRP product professionally, but the platform
kept me mostly in Apex and declarative config — so to go deep on backend systems
engineering I rebuilt the core planning engine myself in Go and Postgres.

The heart of MRP is BOM explosion: you take a production plan for a finished product and
walk its bill-of-materials tree to work out every sub-assembly to build and every raw part
to buy, in what quantities. My first version was the obvious one — recursively walk the
tree in Go, querying each node's children as I reached it. Correct, but a bulk run over 500
plans took almost 26 minutes.

I profiled it and the bottleneck wasn't computation — it was thousands of sequential
database round trips. Classic N+1, in three places: the tree walk, the routing lookups, and
the on-hand-stock netting. I rewrote the traversal as a single recursive CTE in Postgres and
batched the other two into set-based queries. Same output — I verified it byte-for-byte
against the old path — but the bulk run dropped to about a minute and a half. Roughly
17× faster, per-plan from 3.1 seconds to 185 milliseconds, which is the difference between
an overnight batch job and a planner re-planning interactively."

### Why this opener works
- Leads with the honest attribution *first* — defuses the "was this your job?" landmine
  before they can spring it. You look forthright, not caught.
- It's a story with a problem, a diagnosis, and a measured result — not a feature list.
- Every number is real and reproducible. You can pull up `BENCHMARKS.md` if they ask.
- It naturally invites the two best follow-ups (why was it slow / how does the CTE work),
  which you're most prepared for.

### Follow-up Q&A — the questions they'll actually ask

**Q: "Why was the naive version slow — was Go the bottleneck?"**
No — it was entirely I/O-bound, not CPU. Go's own work per node was microseconds; almost all
the time was the goroutine *waiting* on a response from Postgres over a socket. With ~13,000
BOM nodes across 500 plans, each paying a round trip, that waiting dominated. Rewriting it in
a faster language wouldn't have helped — the fix had to remove round trips, not make
computation faster.

**Q: "Why couldn't you just parallelize the round trips with goroutines?"**
Across tree levels you can't — you don't know a node's children until its parent's query
returns, so the queries are a genuine dependency chain, not independent work. You *can*
parallelize siblings within one level, which would turn N round trips into depth-many waves —
a real middle-ground optimization. But the CTE beats that anyway, because it removes the
round trips entirely rather than overlapping them: Postgres does every level of the recursion
in-process, in memory, nanoseconds per step instead of milliseconds per network hop.

**Q: "Walk me through the recursive CTE."**
It has two parts unioned together. The anchor selects the finished product's direct BOM
lines. The recursive term joins the CTE's own output back to bom_lines to get the next level
down, and Postgres repeats that until a level produces no new rows. I carry two extra things
through the recursion: a running quantity — parent quantity times qty-per, so the explosion
math happens in SQL — and an array of visited item IDs. That array is my cycle detection:
if a child already appears in its own ancestry path, I flag it, and the Go side rejects the
whole explosion before writing anything. (See `tree.go`.)

**Q: "How do you detect a cycle in a recursive CTE — doesn't it just loop forever?"**
A plain one would spin until Postgres hits a resource limit. I thread a visited-path array
through each row and check `NOT (child = ANY(path))` in the recursive term, plus a hard depth
cap as a backstop. When a cycle is detected I surface it as an error and roll the transaction
back — nothing partial gets written. That satisfies the requirement that a bad BOM is
rejected *before* any orders are created, not half-created and cleaned up after.

**Q: "How did you know the optimized version was still correct?"**
I kept both implementations and ran them against the same deterministic seed data, then
compared the output row-for-row — production orders, work orders, component requirements,
purchase requests all identical on multiple plans. A faster-but-wrong version is worthless,
so correctness was the gate before I trusted any timing number.

**Q: "What's still slow — where would you go next?"**
The remaining ~185ms/plan is now dominated by individual row INSERTs, which I left unbatched
on purpose so the benchmark isolated the query-pattern change. Next steps: bulk-insert the
orders (Postgres COPY or multi-row INSERT), and move the whole run onto an async worker so
the HTTP request returns immediately and streams progress — that's the piece I'm designing
now. There's also within-plan branch parallelism if I ever needed it, but I'd measure first.

**Q: "You said 26 minutes — on what hardware, what data?"**
500 plans, a 50,000-item catalog, a 2-million-row inventory ledger, Postgres 16 in Docker on
my laptop over a localhost socket. Worth noting localhost is the *best* case for the naive
version — a real deployment paying 0.5–2ms of real network latency per round trip would make
the gap even wider, because the naive cost scales with round-trip count and the CTE's doesn't.

### THE HONESTY BOUNDARY — read before every interview

The submitted resume bullet reads as employer work. It is **not**, and you must not let an
interviewer believe mrp-go ran in production, served real users, or produced business
outcomes. The defensible, and genuinely stronger, position is the one the opener takes: you
work on the real MRP product professionally (true), and you built this Go engine yourself to
go deep on backend engineering the day job didn't cover (true). If asked directly "was this
in production / at your job?" — answer plainly: "The Salesforce product is; this Go rebuild
is my own project." That honesty is not a weakness in the story — it's what makes the whole
thing bulletproof under probing, and it reads as initiative, which is exactly what a strong
candidate signals. Never attach the ¥20M / 35% / uptime numbers to this project; those live
with the real job. If you can't say a number is real and yours, don't say it.

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
- 2026-07-21 — B2 drafted and measured: bulk MRP 500 plans, naive 25m55s → optimized
  1m32s (~16.8×), per-plan 3.11s → 185ms, output verified identical between both paths.
  Full interview script + 6 follow-up Q&As written. Bullet worded with "Backend Services"
  rather than "Microservices" — the architecture is a modular monolith and hasn't earned
  that keyword; gap still open.
