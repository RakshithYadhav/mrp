# Day 2 — Naive MRP explosion & netting: rationale

Read before the scratch recode. This is the naive (v1) MRP engine: `internal/mrp/`
(explosion + netting) plus one HTTP endpoint. It is deliberately unoptimized — Day 3
rebuilds the hot parts (see ADR-0002, BENCHMARKS.md). Sections explain the *why*; a
no-notes self-check is at the bottom.

Verified end-to-end on real seed data (2026-07-20): exploding plan 1 produced 17 production
orders (1 root + 16 children), 63 work orders, 72 component requirements, 29 purchase
requests — all purchase requests targeting buy items only, qty math exact (95 × 5 = 475).

## 0a. `bom_headers` / `bom_lines`, concretely — the lamp example

A **Table Lamp** (`make`) is built from 1× Lamp Base (`make`, itself assembled), 1× Lamp
Shade, 1× Light Bulb, 2m Electrical Wire, 1× Switch (all `buy`).

`bom_headers` — one row per **recipe** (which item, which named pattern):

| id | item_id | name |
|---|---|---|
| 1 | Table Lamp | `STD` |
| 2 | Lamp Base | `STD` |

`bom_lines` — the **ingredients** of one recipe (`bom_header_id` points at the header, not
the item directly):

| id | bom_header_id | child_item_id | qty_per | process_seq |
|---|---|---|---|---|
| 101 | 1 (Table Lamp) | Lamp Base | 1 | 10 |
| 102 | 1 | Lamp Shade | 1 | 30 |
| 103 | 1 | Light Bulb | 1 | 30 |
| 104 | 1 | Electrical Wire | 2 | 20 |
| 105 | 1 | Switch | 1 | 20 |
| 201 | 2 (Lamp Base) | Metal Plate | 1 | 10 |
| 202 | 2 | Ballast Weight | 1 | 10 |

Maps directly to `explode.go`: `bomHeaderFor(TableLamp)` → header 1; `bomLines(1)` → rows
101–105. Lamp Base is `make`, so `explode` recurses: `bomHeaderFor(LampBase)` → header 2;
`bomLines(2)` → rows 201–202. Shade/Bulb/Wire/Switch are `buy` — leaves, straight into
`buyReqs`, no further header lookup. Why a header at all, not a flat
`(parent_item_id, child_item_id, qty)` table: `child_item_id` never appears as a header's
item directly — lines hang off the header's `id`. That's what lets one item have multiple
named recipes later (a `STD` header and a `BUDGET` header for Table Lamp) without smuggling
that distinction into every line row.

**Only `make` items get looked up via `bomHeaderFor` — by convention, not by schema
constraint.** `explode()` is only ever called on confirmed `make` items (root checked via
`ErrNotMakeItem`; recursive calls gated behind `case "make":`) — `buy` items never reach
`bomHeaderFor` at all, they're leaves straight into `buyReqs`. But `bom_headers` has no
`CHECK` tying `item_id` to `item_type = 'make'` — nothing in the schema would reject a
`buy` item having a header row. It's an application-level invariant (the code never
queries it), not a database-level one.

**A real, current gap: multiple active headers for one item have no real tiebreaker.**
Two `bom_headers` rows can share an `item_id` (e.g. `STD` id=1 and `BUDGET` id=3 for
Table Lamp, both `is_active = true`). But `bomHeaderFor`'s query —
`WHERE item_id = $1 AND is_active = true ORDER BY id LIMIT 1` — only filters on
`is_active`, which means "usable at all," not "the default." With two active headers it
silently picks the lower `id`, not necessarily the intended one. This is exactly the
simplification already named in `docs/FRD.md` §7 (the real system's
`CompositionPattern__c.StandardFlag__c` marks the actual default; this schema has no
equivalent). Doesn't bite today — the seeder only ever creates one `STD` header per item —
but a real `is_default` column would be needed before multiple active patterns behave
predictably.

## 0. What the engine does, top to bottom

`POST /api/plans/{id}/mrp` → `mrp.Service.Explode`:
1. Open a transaction; row-lock the plan (`FOR UPDATE`); reject if not `draft`.
2. **Explosion (FR-3):** recursively walk the plan item's BOM tree, creating a production
   order per make node, work orders per routing step, and a component requirement per BOM
   line. Buy children are leaves — their demand is accumulated in a map.
3. **Netting (FR-4):** a *separate pass* — for each buy item, net its accumulated demand
   against on-hand stock and safety stock, apply lot sizing, emit a purchase request if
   short.
4. Flip the plan to `planned`; commit. Either all of it lands, or none (FR-3.5).

## 1. Package placement — why `internal/mrp`, and why it owns its SQL

Day 1's concept doc (§6) said a service layer earns its place "once there's real business
logic to isolate from both HTTP and SQL." This is that moment. `internal/mrp` is the
service/domain layer: the algorithm lives here, not in a handler (which only parses the id
and maps errors to status codes) and not in `internal/repo` (Day 1's CRUD aggregates).

Unlike Day 1, the mrp package issues its **own** SQL directly against the transaction rather
than calling `internal/repo`. Deliberate: every query in an explosion must run inside the
*one* transaction that gives FR-3.5 its atomicity, and the queries are inseparable from the
recursion that drives them (you can't pre-fetch the tree — each level's queries depend on
the previous level's results). Threading a shared `tx` through repo functions that currently
take `*pgxpool.Pool` would mean reworking Day 1's repo signatures — scope creep for no gain.
So the transaction-scoped, algorithm-coupled queries live with the algorithm.

## 2. The recursion — `exploder.explode`

One method, called once per make node, structurally: *create this node's rows, then recurse
into make children.* The shape to internalize:

```
explode(item, qty, parentOrderID):
    cycle-guard on item
    create production_order (parent = parentOrderID)
    create work_orders from routing  → map process_seq → work_order_id
    for each bom_line:
        childQty = qty * qty_per   (÷ (1 - scrap) if scrap)
        create component_requirement on the work order matching line.process_seq
        if child is make: explode(child, childQty, thisOrderID)   ← recursion
        if child is buy:  buyReqs[child] += childQty              ← leaf, for netting
```

**Why buy items are leaves and make items recurse.** A make item is something you build, so
it has its own BOM to explode further. A buy item is purchased — there's nothing to explode
below it. This is also *why this project needs no "low-level coding"* (the textbook
MRP step that processes each shared item once at its lowest level): because it's pegged
per-plan (FRD §7), each make node gets its own production order per occurrence, and buy
demand is only aggregated within this one plan's map — never globally across plans.

**Why `qty` multiplies down the tree.** 95 finished units, a BOM line with `qty_per` 5 →
475 of that component. That component is itself a make item needing 475 → its own children
multiply from 475. The multiplication cascading down the tree *is* the explosion.

## 3. Cycle detection — the `path` set (FR-3.4)

```go
if e.path[it.id] { return ErrCycle }
e.path[it.id] = true
defer delete(e.path, it.id)
```

`path` holds the item ids on the *current recursion path*, not everything ever visited. Add
on entry, remove on return (via `defer`). This is the exact distinction that matters:
- A **cycle** (A contains B contains A) means A is still on the path when we reach it again
  → caught, `ErrCycle`, transaction rolls back before any partial tree persists.
- A **diamond** (X used by two sibling branches) is legal in a DAG, not a cycle — by the
  time the second branch reaches X, the first branch has returned and removed X from the
  path → allowed.

A global "visited-ever" set would wrongly reject diamonds. The `defer delete` is what makes
it a path set instead of a visited set. (Trade-off noted for the concept, not fixed here:
diamonds get re-exploded once per path — pegged production, acceptable at this scale.)

## 4. Work orders and the `process_seq` map (FR-3.2, FR-3.3)

Routing steps become work orders in `seq` order, each linked to its predecessor
(`prev_work_order_id`) — a linked list per order. As they're created, `woBySeq` maps each
step's `seq` to its new work order id. Then each BOM line attaches its component requirement
to `woBySeq[line.process_seq]` — the specific step that consumes it. This is what makes
`process_seq` load-bearing: a component isn't needed "by the order" generally, it's needed
when *its* step starts, which Day 4's scheduling will turn into a real date.

The routing steps are read into a slice *before* the insert loop (via `routingSteps`) rather
than inserted while iterating the query rows — issuing a second query on the same transaction
while a `rows` is still open risks a "conn busy" error. Read fully, close, then write.

## 5. Netting as a separate pass — `net.go` (FR-4, ADR-0002 / AD-2)

Netting runs after the whole tree is walked, over the accumulated `buyReqs` map. Why
separate rather than inline during traversal:
- **Single responsibility / different data shapes.** Traversal queries tree structure
  (`bom_lines`, routings); netting queries an aggregate (`SUM` over `inventory_movements`).
  Different tables, different query shapes — and separating them is exactly what lets Day 3
  *batch* the on-hand lookup into one grouped query instead of interleaving N+1 aggregate
  queries into the walk.
- **No correctness cost here.** In general MRP, a zero net requirement should stop you
  exploding that item's children — which would couple netting into traversal. But netting
  only touches *buy* items (FR-4.1), and buy items are always leaves with no children to
  explode. Make items always get a full production order regardless (pegged). So traversal
  never needs netting's answer to decide whether to keep walking. State this explicitly if
  asked — it's the obvious objection to separating the passes, and it doesn't apply here.

### The safety-stock sign — a spec bug caught while implementing

FR-4.1 originally read `net = max(0, gross − on_hand − safety_stock)`. **That sign was
wrong.** The correct form, now in both the code and the FRD (fixed 2026-07-20):

```
net = gross − on_hand + safety_stock
```

Reasoning: safety stock is a reserve you must *not* consume. So the stock actually available
to meet demand is `on_hand − safety_stock`, and `net = gross − (on_hand − safety_stock) =
gross − on_hand + safety_stock`. Worked: on-hand 50, safety 10, gross 200 → available 40 →
net 160. The original `− safety_stock` gives 140 — it under-orders and silently eats into
the buffer, which defeats the entire point of holding safety stock.

Worth keeping as an interview story: the spec, the fundamentals doc, and the code disagreed,
and the disagreement was caught by deriving the formula from *what safety stock is for*
rather than transcribing it. That's the difference between implementing a spec and
understanding a domain.

### Lot sizing (FR-4.3)

`lot_for_lot` orders exactly the net. `fixed` rounds up to the next multiple of
`fixed_lot_size` via `math.Ceil(net / lot) * lot`. Buy 47 needed, lot 50 → order 50.

## 6. Transaction, atomicity, and the `FOR UPDATE` lock

The whole run is one transaction opened in `Explode`, with `defer tx.Rollback` as the
Day-1 safety net — any error return anywhere rolls back the entire tree (FR-3.5), and only
the final `tx.Commit` makes it real.

**What `FOR UPDATE` does, mechanically.** A plain `SELECT` doesn't lock anything — two
transactions can both read the same row with no blocking. `FOR UPDATE` means "I'm reading
this row *to* change something based on it — lock it." The first transaction to run
`SELECT ... FOR UPDATE` on a row acquires an exclusive lock on that row; any other
transaction running `FOR UPDATE` (or `UPDATE`/`DELETE`) on the *same row* blocks — queued,
waiting — until the first commits or rolls back.

**The race it closes.** Without it: two near-simultaneous requests could both read
`status = 'draft'` before either commits, both think it's safe, both explode the same plan
— duplicate orders. Classic check-then-act race. With `FOR UPDATE`: request A locks and
proceeds; request B's identical `SELECT` blocks entirely; A finishes, flips status to
`planned`, commits, releasing the lock; *only then* does B's blocked `SELECT` return — with
`status` already `planned` — so B's `if plan.status != "draft"` check correctly rejects it
(`ErrAlreadyExploded`, 409). This is exactly what the earlier re-run test demonstrated.

**Row-level, not table-level — this is NFR-5 directly.** The lock only affects *this one
plan's row*; exploding plan 2 concurrently is entirely unaffected. "Concurrent runs on
different plans never block each other; concurrent runs on the same plan are prevented
outright, not raced" — both halves come from this one clause.

**Honest scope:** this works because everything is synchronous, one transaction, one
process. It's a first cut at FR-6.2 (one run per plan) — once Day 3 moves execution to an
async worker (possibly multiple), a transaction-scoped row lock isn't quite enough on its
own, which is exactly what AD-4 (advisory lock vs. a partial unique index on `mrp_jobs`) is
for. Right tool for the naive version; not the final answer once the job runner exists.

## 7. Deliberate scope cuts (name them, don't hide them)

- **No real dates yet.** Every order's `due_date` is the plan's due date (placeholder);
  `start_date`, `planned_start/end` are null. Day 4 backward scheduling fills real dates.
- **No re-explosion.** A plan is exploded once (draft → planned); re-running is rejected,
  not diff-applied. The real system has dedicated deletion batches for this.
- **Synchronous.** The HTTP request blocks on the whole run — which *is* part of the naive
  story. Day 3's async worker + SSE is the fix.
- **Diamonds re-explode per path** (§3). Acceptable for pegged production at this scale.

## 8. Why `exploder` is a struct, not free functions

`explode()` recurses once per make item, and every level of that recursion needs to read and
mutate the *same* shared state — the transaction, the cycle-guard set, the buy-demand
accumulator, running counters. A struct with methods on `*exploder` threads that through
without a long, easy-to-misorder parameter list at every recursive call site.

The fields split into two categories ([explode.go:17-28](../../internal/mrp/explode.go)):
- **Fixed context, set once at construction** (`mrp.go`'s `Explode`): `tx`, `planID`,
  `dueDate`. Every level needs them (every `production_order` insert needs the plan id and a
  due date) but they never change — stored once instead of re-passed down every call.
- **Mutable, shared across the whole call tree**: `path` (added/removed at every depth),
  `buyReqs` (accumulated from leaves scattered across the entire tree), and the three
  `count*` fields (incremented at any depth, read back by `Explode` for the `Result`).

Why not plain function parameters instead of struct fields? `path` and `buyReqs` are maps —
reference types in Go, so they'd stay shared even as plain params. But the `count*` fields
are plain `int`; mutating those across recursive calls without a struct field would mean
threading `*int` pointers through every call instead — uglier than a receiver. And bundling
keeps the recursive call site (`e.explode(ctx, child, childQty, orderID)`) carrying only what
actually *varies* per call — the item, its qty, its parent order — with everything constant
or shared living on the receiver.

**One `exploder` per `Explode()` call, never shared/global.** `Explode` can run concurrently
for different plans (different HTTP requests), so each call constructs its own
`&exploder{...}` scoped to exactly one transaction and one plan — safe by construction, no
locking needed, and it dies when `Explode` returns.

**Why `tx` (not `pool`) lives on the struct.** Every query at every recursion depth, plus the
netting pass, must run inside the *one* transaction opened in `Explode` — that's what makes a
rollback anywhere undo the entire tree (FR-3.5). Methods taking a `pool` instead would have
no way to guarantee they all share that one transaction.

## Self-check — answer cold, then compare

1. Why do buy items become leaves while make items recurse — and how does that connect to
   why this project needs no "low-level coding"?
2. What exactly is in the `path` map, when is an id added and removed, and why does that
   distinction let a diamond through but reject a cycle?
3. What would break if `path` were a global "visited-ever" set instead?
4. Why is netting a *separate* pass, and what's the one objection to separating it that
   turns out not to apply here — and why doesn't it apply?
5. Is `net = gross − on_hand − safety_stock` correct? Derive the right formula and give a
   worked number.
6. Why are routing steps read fully into a slice before the work-order insert loop, instead
   of inserting while iterating the query rows?
7. What does `woBySeq` map, and what makes `process_seq` load-bearing rather than cosmetic?
8. What does `SELECT ... FOR UPDATE` on the plan row buy you, and which future requirement
   does it partially satisfy?
9. Which parts of this are the deliberate "naive" bits that Day 3 optimizes, and what does
   each become?
10. If `tx.Commit` is never reached because an error fires mid-tree, what happens to the
    orders already inserted, and which line guarantees it?
11. Which `exploder` fields are fixed context set once at construction, and which are mutable
    state shared across the whole recursive call tree?
12. Why couldn't the `count*` fields be plain function parameters the way `qty` and
    `parentOrderID` are, given that `path` and `buyReqs` manage fine as shared maps?
13. Why does `exploder` store `tx pgx.Tx` rather than `*pgxpool.Pool`, and what would break
    (concretely, w.r.t. FR-3.5) if it stored the pool instead?
