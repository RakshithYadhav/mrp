# FR-5 — Backward Scheduling: what it actually requires

## The exact requirement (FRD §5, FR-5)

- **FR-5.1** The plan's due date sets the due date of the **root** production order (the
  plan's own item, `parent_order_id IS NULL`) — specifically that order's *last* routing
  step. "Root" is the top of the pegging tree, not the deepest node; see the trap section
  below, this is the one people get backwards.
- **FR-5.2** Each work order's start = its due date − step duration (`setup_hours +
  hours_per_unit × qty`), walked backward across the plant's calendar, skipping holidays
  and non-working hours.
- **FR-5.3** A child production order's due date equals the start date of the parent work
  order step that consumes it.
- **FR-5.4** A purchase request's `need_by` equals the start date of the earliest work
  order requiring that component.

That's the whole spec. Four sentences, but each one is doing real work — read on for why.

## The problem this solves

Right now (post Day 2/3), exploding a plan produces a full tree — production orders, work
orders, component requirements, purchase requests — but every single row in that tree
carries the *same* date: the plan's one `due_date`. Check `explode.go:246` and
`net.go`/`net_optimized.go` — `e.dueDate` gets stamped onto every production order's
`due_date` and every purchase request's `need_by`, root or leaf, level 1 or level 4,
identically.

That's not useful to anyone standing on a shop floor. "100 wheels, due day 10" doesn't tell
a buyer when to actually place a purchase order, or tell an operator when to actually start
assembling. If everything is due "day 10," and spoke procurement has a 3-day lead time,
someone reading this tree with no scheduling logic would place that spoke order on day 10
too — three days too late to have spokes in hand when assembly needs them.

This is [[mrp-fundamentals]] step 4 (lead-time offsetting), made concrete: the fix is to
walk backward from the one date you're *given* (the plan's due date) through the one
resource that actually consumes time (routing steps), assigning every dependent record its
own, earlier, correct date.

## Why *backward*, not forward

Two ways to schedule anything with a deadline:

- **Forward scheduling** — start from "when could this begin?" (e.g., today) and push
  dates forward, telling you the earliest possible completion date. Useful when you don't
  have a fixed deadline yet.
- **Backward scheduling** — start from a *known* required date and walk backward, telling
  you the latest possible start date that still hits the deadline.

MRP is backward scheduling by construction, because the input is never "start whenever" —
it's always "customer needs 100 bikes by day 10." The due date is the one fixed point;
every other date in the tree is derived by subtracting duration from it, level by level,
moving *earlier* as you go deeper into the tree (further from the finished good, further
from the deadline).

This is also **infinite loading** — a named simplification, same honesty-boundary pattern
as low-level coding and cross-plan netting in `docs/FRD.md` §7. FR-5.2 subtracts routing
duration and skips holidays/non-working hours, but never checks whether a resource
(`resources.capacity_hours_per_day`) is already fully booked on that day by some *other*
work order. Real finite scheduling would need to track resource load across all jobs
competing for the same machine/person, and shift a work order's date if its resource is
already saturated. FR-5 doesn't do that — say so plainly if asked, don't imply otherwise.

## Worked example (continuing the wheel/bike numbers from mrp-fundamentals.md)

100 bikes due day 10. Bike = 1 frame + 2 wheels (assembly step, 2-day duration). Wheel = 36
spokes (assembly step, 3-day duration). Wheel on-hand 50.

```
Bike production order:      due day 10 (= plan due date, FR-5.1)
  Bike "assemble" step:     starts day 8  (10 − 2 day duration, FR-5.2)

Wheel production order:     due day 8    (= bike assemble step's start, FR-5.3)
  Wheel "assemble" step:    starts day 5  (8 − 3 day duration, FR-5.2)

Spoke purchase request:     need_by day 5 (= wheel assemble step's start, FR-5.4)
```

Each level's *start* date becomes the next level down's *due* date. That's FR-5.3 doing the
propagation — it's the only rule of the four that reaches across the parent/child boundary;
FR-5.1/5.2/5.4 all operate within one production order's own tree of work order steps.

## A wrinkle worth seeing now: two different date grains

- `production_orders.due_date` / `start_date` and `purchase_requests.need_by` are all
  `DATE` — day granularity, no time-of-day.
- `work_orders.planned_start` / `planned_end` are `TIMESTAMPTZ` — they carry an actual
  clock time, because FR-5.2 walks backward through **hours** (`setup_hours +
  hours_per_unit × qty`), bounded by `plants.work_start`/`work_end` within a day, rolling
  over to the previous working day (skipping `holidays`) once a day's working hours are
  exhausted.

So the backward walk itself operates at hour resolution (a work order might start at
2:30pm on day 8, not just "day 8"), but once you propagate a *start* up to become a child
order's *due date* (FR-5.3) or a purchase request's *need_by* (FR-5.4), you're writing into
a `DATE` column — so only the date part survives, the hour is dropped at that boundary.

## Relevant schema (already exists, unused until now)

- `plants(work_start, work_end)` — one plant's daily working window.
- `holidays(plant_id, holiday DATE)` — non-working calendar days, per plant.
- `routing_steps(setup_hours, hours_per_unit)` — what FR-5.2's duration formula reads.
- Link path from a plan to its calendar: `production_plans.warehouse_id →
  warehouses.plant_id → plants` (+ `holidays` on that same `plant_id`).

## A common mix-up: two different trees, easy to conflate

FR-5.1 anchors the plan's due date to the root production order's *last* work order. Two
relationships in the schema look similar but aren't:

- `production_orders.parent_order_id` — one row **per item occurrence** in the BOM tree.
  Root = the plan's own item (`parent_order_id IS NULL`). A plan has exactly **one**
  `item_id` (`production_plans.item_id`), so there is exactly **one root order per plan** —
  never a set of sibling root orders.
- `work_orders.prev_work_order_id` — a linked list of **routing steps within one
  production order** (e.g. weld → paint → assemble for that one item). This is the chain
  FR-5.2 walks backward across.

So "root production order's last work order" = within the **root** order's own
`prev_work_order_id` chain, the terminal step (`MAX(seq)` for that `production_order_id`,
nothing pointing forward from it). Unambiguous specifically *because* one production
order's work orders form a linked list, never parallel/branching steps, in this model — a
routing with converging parallel branches would need a DAG join to define "last," out of
scope here (see the "not done" list below).

### The trap: `parent_order_id` is consumption, not sequence

Take a chain of make items — PO1 is the plan's item, PO2 is its component, PO3 is PO2's
component. Assume one routing step each, so one work order per order:

```
PO1 (root, parent_order_id = NULL)  — WO1
  └─ PO2 (parent = PO1)             — WO2
        └─ PO3 (parent = PO2)       — WO3
```

The tempting reading is "WO3 is last, so anchor the plan's due date to WO3 — it depends on
WO1 and WO2 finishing first." **That's exactly backwards.**

`parent_order_id` records *what consumes what*, not *what runs after what*. PO2 is PO1's
child because PO1's item **needs PO2's item as a component** — so PO2's output must be
finished and on the shelf **before** PO1's consuming step can start. Execution therefore
runs **leaf → root**:

```
PO3 finishes FIRST  → feeds PO2's consuming step
PO2 finishes NEXT   → feeds PO1's consuming step
PO1 finishes LAST   → PO1 IS the finished good, so it lands on the plan's due date
```

So WO1 — the root order's own (and here only, hence last) work order — is what FR-5.1
anchors to the plan due date. WO2 and WO3 get progressively **earlier** dates via FR-5.3 as
you descend. Dates decrease going *down* the tree; the deepest node is scheduled earliest,
not latest.

Same shape as the wheel/bike worked example above: Bike (root) day 10 → Wheel (child) day 8
→ Spoke (grandchild) day 5. Depth increases, dates move earlier.

If it helps to sanity-check the direction: the root is the only order whose completion the
*customer* sees. Nothing consumes the root's output, so nothing can be scheduled after it —
which is precisely what makes it the anchor point for backward scheduling.

## Full worked example: the lamp, plan → orders → work orders

Same BOM as `day-02-mrp-explosion.md` §0a, now with routings and dates attached. This is the
one to draw at a whiteboard.

**Masters (input, already in the DB):**

Table Lamp `STD` routing — 3 steps. Lamp Base `STD` routing — 2 steps.

| routing | seq | step name | consumes (from `bom_lines.process_seq`) |
|---|---|---|---|
| Table Lamp | 10 | Fit Base | Lamp Base ×1 |
| Table Lamp | 20 | Wire | Electrical Wire ×2, Switch ×1 |
| Table Lamp | 30 | Assemble & Test | Lamp Shade ×1, Light Bulb ×1 |
| Lamp Base | 10 | Press & Weld | Metal Plate ×1, Ballast Weight ×1 |
| Lamp Base | 20 | Deburr & Paint | — |

**Plan (input):** build **100 Table Lamps, due day 10**. One row in `production_plans`
(`item_id` = Table Lamp, `qty` = 100, `due_date` = day 10).

**What explosion + scheduling produce:**

```
production_plans #1  — Table Lamp ×100, due day 10
│
└── production_orders #1  — Table Lamp ×100      [ROOT: parent_order_id = NULL]
    │                       due day 10 ← plan due date (FR-5.1)
    │                       start day 6
    │
    ├── work_orders #1  seq 10  "Fit Base"          day 6 → day 7   prev = NULL
    │     └── component_requirements: Lamp Base ×100
    ├── work_orders #2  seq 20  "Wire"              day 7 → day 8   prev = WO#1
    │     └── component_requirements: Elec. Wire ×200, Switch ×100
    └── work_orders #3  seq 30  "Assemble & Test"   day 8 → day 10  prev = WO#2
          └── component_requirements: Lamp Shade ×100, Light Bulb ×100
              ↑ LAST work order of the ROOT order — its end IS the plan due date
    │
    └── production_orders #2  — Lamp Base ×100   [parent_order_id = #1]
          due day 6 ← start of WO#1, the step that consumes Lamp Base (FR-5.3)
          start day 3
          │
          ├── work_orders #4  seq 10  "Press & Weld"     day 3 → day 5   prev = NULL
          │     └── component_requirements: Metal Plate ×100, Ballast ×100
          └── work_orders #5  seq 20  "Deburr & Paint"   day 5 → day 6   prev = WO#4
                ↑ last work order of THIS order — but this order is not the root,
                  so FR-5.1 does not apply to it; FR-5.3 gave it its due date
```

**Purchase requests** (FR-5.4 — `need_by` = start of the earliest work order needing it;
quantities shown gross, netting per FR-4 reduces them by on-hand):

| item | qty | needed by WO | `need_by` |
|---|---|---|---|
| Metal Plate | 100 | WO#4 (starts day 3) | **day 3** |
| Ballast Weight | 100 | WO#4 (starts day 3) | **day 3** |
| Electrical Wire | 200 | WO#2 (starts day 7) | **day 7** |
| Switch | 100 | WO#2 (starts day 7) | **day 7** |
| Lamp Shade | 100 | WO#3 (starts day 8) | **day 8** |
| Light Bulb | 100 | WO#3 (starts day 8) | **day 8** |

### The five things this picture is meant to make obvious

1. **One plan → one root order.** `production_plans` has a single `item_id`, so PO#1 is the
   only order with `parent_order_id = NULL`. Everything else descends from it.
2. **Only the root's last work order touches the plan due date.** WO#3 ends day 10. WO#5 is
   also "the last work order of its order" — but PO#2 isn't the root, so FR-5.1 is silent
   about it; it got day 6 from FR-5.3 instead.
3. **The deeper order runs *earlier*.** PO#2 occupies days 3→6; PO#1 occupies days 6→10.
   The child finishes exactly when the parent's consuming step begins — that handoff at
   day 6 is FR-5.3 in one number.
4. **`process_seq` is load-bearing, not decoration.** Lamp Base hangs off step 10, not off
   "the order." That's the only reason PO#2 is due day 6 and not day 8 or day 10. Move that
   BOM line to `process_seq` 30 and the entire Lamp Base sub-tree shifts two days later.
5. **Purchase dates spread out.** All six buy items would read "day 10" pre-FR-5. Now the
   plate buyer works to day 3 and the shade buyer to day 8 — five days apart, from one plan
   date, purely from routing durations.

## `prev_work_order_id` — the chain inside one production order

`work_orders.prev_work_order_id` is a **self-referencing, nullable FK back to
`work_orders.id`**. It makes each production order's steps a **singly linked list that
points backward**: every step names the step immediately before it, and the first step
names nothing.

From the lamp example:

| id | production_order_id | seq | name | prev_work_order_id |
|---|---|---|---|---|
| 1 | 1 (Table Lamp) | 10 | Fit Base | `NULL` ← first step |
| 2 | 1 | 20 | Wire | 1 |
| 3 | 1 | 30 | Assemble & Test | 2 ← last step |
| 4 | 2 (Lamp Base) | 10 | Press & Weld | `NULL` ← first step of a *different* chain |
| 5 | 2 | 20 | Deburr & Paint | 4 |

Two separate chains. `prev_work_order_id` **never crosses a production order boundary** —
WO#4 starts a fresh chain with `NULL` rather than pointing at WO#3.

### What it means physically

WO#2 cannot start until WO#1 is finished — the *same physical units* flow through the
steps in order. You can't wire a lamp before its base is fitted. That is a fundamentally
different relationship from `parent_order_id`:

| | relates | means |
|---|---|---|
| `prev_work_order_id` | two steps of the **same** item's build | **sequence** — same units, one after another |
| `parent_order_id` | two orders for **different** items | **consumption** — child's output becomes parent's input |

### How the code builds it — `explode.go:90-101`

```go
var prevWO *int64                                          // nil ⇒ SQL NULL

for _, step := range steps {                               // steps sorted ORDER BY s.seq
    woID, err := e.insertWorkOrder(ctx, orderId, step, qty, prevWO)
    ...
    workOrderBySeq[step.seq] = woID
    prevWO = &woID                                         // this row becomes next row's prev
}
```

Three things carry weight here:

1. **`prevWO` is declared *inside* `explode`, per production order** — not on the exploder
   struct. It resets to `nil` on every recursive call, which is exactly what starts each
   order's chain fresh. Hoisting it to a field would chain WO#4 onto WO#3 and silently
   weld two unrelated orders' routings into one list.
2. **`*int64`, not `int64`.** The first step genuinely has no predecessor, and that must
   land as SQL `NULL`. A plain `int64` would insert `0` — and since `prev_work_order_id`
   is a real FK to `work_orders(id)`, `0` matches no row and the insert would fail the
   constraint. Pointer-to-nil is how Go says "absent" to pgx, distinct from "zero."
3. **The chain follows `seq` only because `routingSteps` sorts by it** —
   `ORDER BY s.seq` at `explode.go:200`. Drop that `ORDER BY` and the linked list would
   still be *built*, just in whatever order Postgres returned rows: a chain that no longer
   matches the routing. The sort is load-bearing correctness, not cosmetics.

### Why store it when `seq` already orders the steps?

Fair challenge, and worth being honest: **today it is derivable.** Given
`(production_order_id, seq)` you could compute the predecessor with a window function
(`LAG(id) OVER (PARTITION BY production_order_id ORDER BY seq)`). So it *is* denormalized.

What it buys:

- **Survives renumbering.** Routings are conventionally numbered 10/20/30 so steps can be
  inserted between them later. Explicit adjacency doesn't care what the numbers are or
  whether gaps appear.
- **O(1) predecessor lookup** for FR-5.2's backward walk — follow the pointer, no
  self-join or window function per step.
- **Mirrors the source system** (`WorkOrder__c`'s previous-order lookup), consistent with
  the rest of this schema's lineage.

### Direction matters for FR-5.1

The pointers face **backward**, so the two ends are not equally easy to find:

- **First step** — trivial: `WHERE prev_work_order_id IS NULL`.
- **Last step** — no row stores "I am last." You find it either as `MAX(seq)` for that
  production order, or as the row **no other row points at**.

That asymmetry is why FR-5.1 says "last work order" and the practical implementation
reaches for `MAX(seq)` — and it's only unambiguous because this model has no branching
routings (see the trap section above).

### A gap worth naming

`UNIQUE (production_order_id, seq)` stops duplicate step numbers within an order. But
**nothing in the schema enforces that `prev_work_order_id` points at a work order belonging
to the same production order** — the FK only checks the target row exists. Nor does
anything prevent a cycle (A points to B, B points to A). Both are application-level
invariants held only by the loop above, same category as the `bom_headers` / make-item
invariant named in `day-02-mrp-explosion.md` §0a. A `CHECK` can't express it; enforcing it
would need a composite FK on `(production_order_id, prev_work_order_id)` against a matching
unique key, or a trigger.

## The real precedence DAG spans production orders

A natural but wrong conclusion from the section above: *"work order dependencies live only
inside a production order; a WO in PO#1 and a WO in PO#2 are unrelated."*

The first half is right about **storage**. The second half is wrong about **reality**.

There are **two kinds of precedence edge**, and only one of them is a column:

| edge | stored as | crosses PO? |
|---|---|---|
| step → next step | `prev_work_order_id` (explicit FK) | never |
| child order's last step → parent's consuming step | *derived* — `parent_order_id` + `component_requirements.work_order_id` | always |

The second edge is real, load-bearing, and already visible in the lamp numbers: WO#5 ends
day 6, WO#1 starts day 6. Not a coincidence — FR-5.3 *is* that edge, written as a date.

Interactive diagram (Excalidraw):
<https://excalidraw.com/#json=qoEOohhR2P_aW0hvrfs93,TTYdR_O6txPysKJ0grPjSA>

### The lamp's actual graph

```
        [PO#2 — Lamp Base]                    [PO#1 — Table Lamp, ROOT]

  WO#4 ──prev──▶ WO#5 ─ ─ ─component─ ─ ─▶ WO#1 ──prev──▶ WO#2 ──prev──▶ WO#3
  day 3-5        day 5-6                   day 6-7        day 7-8        day 8-10
                                                                            ▲
   ══ solid: prev_work_order_id (within one PO)                             │
   ─ ─ dashed: derived cross-PO edge (WO#1 consumes Lamp Base,        plan due date
               so PO#2 must complete first)                            anchors here
```

One connected graph, five work orders, spanning two production orders. Scheduling walks
this whole thing — it is *not* five independent per-PO walks.

### Which WO does the cross-PO edge land on?

**Whichever step's `process_seq` consumes that component** — not automatically the parent's
first step. Lamp Base sits on `bom_lines.process_seq = 10`, so the edge targets WO#1. Move
that BOM line to `process_seq = 30` and the same edge retargets to WO#3, and the whole Lamp
Base sub-tree slides later. The pointer is `component_requirements.work_order_id`, set from
`workOrderBySeq[line.process_seq]` during explosion.

### The shape: an in-tree converging on one sink

Worth working out, because it explains FR-5.1 structurally rather than by assertion.

- **Out-degree of any WO is at most 1.** A non-final step points to the next step in its
  own PO. A final step has no next step — but if its PO is a child, it points at its
  parent's consuming step. Never both.
- **In-degree can exceed 1.** A step consuming three `make` components receives three
  cross-PO edges, plus one `prev` edge from the step before it.

Many-in, one-out, no cycles ⇒ an **in-tree**: every path flows toward a single sink. That
sink is the root production order's last work order — the only WO in the entire plan with
no successor at all, because nothing consumes the root's output.

So FR-5.1 isn't an arbitrary convention. It anchors at the graph's unique sink, which is
the only node a backward walk *can* start from.

### Where purchase requests fit

Buy items are the graph's sources — external supply, no producing WO. FR-5.4 attaches each
one to the start of the earliest WO requiring it (Metal Plate → WO#4 → day 3). Sources feed
in at the leaves, everything converges on WO#3.

### Why it's a tree and not a wider DAG

Because this system is **pegged** (seiban, `docs/FRD.md` §7): a `make` child gets its own
production order *per BOM line occurrence*, so one child PO feeds exactly one consuming WO
— one outgoing edge, hence a tree.

A system doing textbook cross-order netting would build **one** shared PO for a component
needed by several parents. That PO's last WO would then have *several* outgoing edges, and
the graph would become a genuine DAG with fan-out — requiring a topological sort to
schedule, instead of the recursive tree walk that suffices here. Pegging is what buys the
simpler traversal.

## Thought experiment: what if *every* production order had `parent_order_id = NULL`?

Worth walking, because it isolates exactly what the pegging link is buying — and because
nothing currently stops it.

### It is not prevented by anything

`parent_order_id BIGINT REFERENCES production_orders(id)` is nullable with no further
constraint. There is **no** rule saying "exactly one row per `plan_id` may have a NULL
parent." A plan whose orders are all roots inserts cleanly — no FK violation, no error.

The mechanism is a sentinel in `insertProductionOrder` (`explode.go:240-243`):

```go
var parent any
if parentOrderId != 0 {      // 0 means "no parent"
    parent = parentOrderId
}                            // otherwise `parent` stays nil ⇒ SQL NULL
```

The root is invoked with `0`; children receive `orderId` threaded down the recursion. If
that threading breaks — a refactor passing `0`, a caller forgetting to forward it — every
order silently becomes a root. `0` is not a valid id, so a bug can't be caught by the FK;
it's laundered into `NULL` before it ever reaches Postgres.

Note the inconsistency worth owning: the same file expresses "absent" two different ways —
`*int64` + `nil` for `prevWO`, `int64` + `0` sentinel for `parentOrderId`. The pointer
version is the safer idiom; the sentinel version is what makes this failure invisible.

### What actually breaks

1. **The pegging tree becomes a forest.** N production orders, N roots, zero edges between
   them — not one tree with N−1 links.
2. **FR-5.1 loses its referent.** "The root production order" now has N candidates. There
   is no unique sink, so a backward walk has no defined starting node.
3. **FR-5.3 has nothing to read.** A child's due date comes from *its parent's* consuming
   step. No parent ⇒ no step ⇒ no date to inherit. The rule can't fire.
4. **The cross-PO edges vanish.** Those edges were derived from `parent_order_id` +
   `component_requirements.work_order_id`. Remove the first term and the connected in-tree
   shatters into N isolated `prev_work_order_id` chains.
5. **You land back in the pre-FR-5 state** described at the top of this doc: every order
   stamped with the plan's one `due_date`, plate buyer and shade buyer both told "day 10."

Notice these are all the *same* failure viewed from different angles: without the parent
link, there is no graph — only disconnected fragments — and scheduling is fundamentally a
graph walk.

### The honest nuance: flat orders aren't inherently wrong

Textbook, non-pegged MRP genuinely does produce flat production orders. In that model
order-to-order links don't exist at all: a component needed by three parents gets **one**
shared order, and sequencing comes from **low-level coding** — every item is assigned its
deepest level in any BOM, and the whole plan is processed level by level, so an item is
never scheduled until every parent that consumes it has been.

So "all roots" isn't nonsense — it's a different, legitimate architecture, the one
`docs/FRD.md` §7 explicitly declines. What *is* nonsense is landing there by accident: this
codebase has no low-level codes and no level-by-level pass, so flat orders here mean the
dependency information is simply gone, not relocated.

### How to make it impossible

A partial unique index states the invariant directly:

```sql
CREATE UNIQUE INDEX one_root_per_plan
    ON production_orders (plan_id)
    WHERE parent_order_id IS NULL;
```

One NULL-parent row per plan, enforced by Postgres. The second root fails the insert, so a
broken recursion aborts the transaction (FR-3.5 rolls it back) instead of persisting a
silently dateless tree. This is a genuinely good use of a partial index — the constraint
applies only to the subset of rows where the predicate holds, which a plain `UNIQUE
(plan_id)` could never express.

Detection query for existing data:

```sql
SELECT plan_id, count(*)
FROM production_orders
WHERE parent_order_id IS NULL
GROUP BY plan_id
HAVING count(*) > 1;
```

This sits in the same family as the other unenforced invariants already named — the
`bom_headers`/make-item one (`day-02-mrp-explosion.md` §0a) and the
`prev_work_order_id`-stays-within-its-PO one above. Difference: this one has a cheap,
purely declarative fix, so it's the strongest candidate of the three to actually add.

## What FR-5 does *not* do (name these if asked)

- No capacity/resource-contention check (infinite loading, see above).
- No forward-scheduling mode — always backward from the due date.
- No rescheduling once written — if a plan's due date changes after explosion, FR-5 as
  specified doesn't define a re-schedule path; that would be a new AD if it comes up.

## Self-check (no notes)

1. Why is scheduling in this system backward, not forward, given what the input to a plan
   always is?
2. A plan produces exactly one root production order — why, structurally, can't it produce
   three sibling root orders?
3. What's the difference between `production_orders.parent_order_id` and
   `work_orders.prev_work_order_id` — which tree does each one build?
4. In a root order with steps weld → paint → assemble, which one does FR-5.1 anchor to, and
   why is "last" unambiguous here but wouldn't be for a routing with parallel branches?
5. Given a chain PO1 → PO2 → PO3 (each the parent of the next, one work order each), which
   work order gets the plan's due date, and which production order is *executed* first?
   Explain why `parent_order_id` doesn't mean "runs after."
6. A child production order's due date isn't always derived from the root order's *last*
   step — why not, per FR-5.3?
7. Name the two date-grain columns FR-5 writes into, and say why the hour component
   survives on one but gets dropped on the other.
8. What does "infinite loading" mean here, and which FR-5 sub-rule is silent about it?
9. In the lamp example, WO#5 is the last work order of production order #2 — why doesn't
   FR-5.1 anchor the plan due date to it? Which rule gave it its date instead?
10. The Lamp Base BOM line sits at `process_seq` 10. If you moved it to `process_seq` 30,
    what happens to production order #2's due date, its work orders, and the `need_by` on
    the Metal Plate purchase request?
11. Without looking: reconstruct why Metal Plate is needed day 3 but Light Bulb day 8, when
    both feed the same plan due day 10.
12. Why is `prevWO` declared inside `explode` rather than as a field on `exploder`? What
    exactly breaks if you hoist it?
13. Why is the parameter `*int64` instead of `int64`, and what specifically goes wrong at
    the database level with the plain value type?
14. `prev_work_order_id` is derivable from `(production_order_id, seq)` with a window
    function — so justify storing it anyway.
15. Why is "find the first work order" a trivial query but "find the last" not? What does
    that asymmetry have to do with FR-5.1?
16. Which invariant about `prev_work_order_id` does the schema *not* enforce, and what
    would it take to enforce it?
17. True or false: two work orders in different production orders have no dependency on
    each other. Defend your answer, and name both edge types in the real precedence graph.
18. Only one of the two edge types is a stored column. How is the other one derived, and
    from which two pieces of schema?
19. Show that every work order has out-degree ≤ 1 but can have in-degree > 1. What graph
    shape does that force, and how does that shape *prove* FR-5.1's anchor point rather
    than just asserting it?
20. If this system did cross-order netting instead of pegging, what would happen to the
    out-degree of a shared component's last work order — and why would that force a
    topological sort where a recursive tree walk works today?
21. If every production order in a plan had `parent_order_id = NULL`, which of FR-5.1–5.4
    could still execute? Explain why the ones that can't are all really the same failure.
22. Why can't the `0`-sentinel bug in `insertProductionOrder` be caught by the foreign key?
    What does the same file do differently for `prevWO`, and which idiom is safer?
23. Write the partial unique index that enforces one root per plan. Why can't a plain
    `UNIQUE (plan_id)` express this?
24. A real MRP system *can* legitimately have flat, unparented production orders — what
    machinery does it need to make that work, and why would flat orders be a bug in *this*
    codebase specifically?
