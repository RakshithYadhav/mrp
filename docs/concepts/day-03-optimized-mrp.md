# Day 3 — Optimized MRP: the recursive CTE and batched loads

Read this before the scratch recode. Start with §1 (`tree.go`) — it's the hardest and most
important part, the actual AD-1 decision made real. §2–3 (the Go consumers) are much
shorter reads: same shape as Day 2's `explode.go`/`net.go`, just fed from memory instead of
the database. A no-notes self-check is at the bottom.

Measured result this optimizes toward (`BENCHMARKS.md` §1, verified real):
**500 plans, 25m55.27s (naive) → 1m32.4s (optimized), ~16.8×, per-plan 3.11s → 185ms.**

## 0. What changed, in one sentence per file

- `tree.go` (new) — one recursive CTE loads the *entire* BOM tree in one round trip,
  replacing Day 2's per-node `bomHeaderFor`/`bomLines` calls. Also batches item, BOM-header,
  and routing-step lookups that were one-query-per-node in Day 2.
- `explode_optimized.go` (new) — same order-creation logic as Day 2's `explode()`, but every
  lookup is now a map read against data already loaded, not a query.
- `net_optimized.go` (new) — same netting math as Day 2's `net()`, but on-hand stock is one
  grouped query for every buy item instead of one query each.
- `mrp_optimized.go` (new) — `ExplodeOptimized`, identical contract to Day 2's `Explode`
  (same guards, same atomicity, same `Result`), calling the optimized internals instead.
- **Nothing in Day 2's files changed.** Both versions are kept, on purpose — see §4.

## 1. `tree.go` — the recursive CTE, read this first

### 1a. What a CTE is, briefly (skip if solid)

`WITH name AS (...)` gives a temporary named result you can `SELECT` from within one query
— a reusable subquery. `WITH RECURSIVE` adds one thing a plain CTE can't do: the query is
allowed to reference *itself* inside its own definition. That self-reference is what makes
tree-walking possible in one round trip.

### 1b. The query, piece by piece

```sql
WITH RECURSIVE exploded AS (
    -- ANCHOR: the root item's direct BOM lines
    SELECT
        bh.item_id AS parent_item_id,
        bl.child_item_id,
        bl.process_seq,
        ($2::numeric * bl.qty_per / (1 - bl.scrap_pct / 100.0)) AS child_qty,
        1 AS depth,
        ARRAY[bh.item_id] AS path,
        false AS cycle_detected
    FROM bom_headers bh
    JOIN bom_lines bl ON bl.bom_header_id = bh.id
    WHERE bh.item_id = $1 AND bh.is_active = true

    UNION ALL

    -- RECURSIVE TERM: for each non-cycle row so far, find its own child's lines
    SELECT
        e.child_item_id AS parent_item_id,
        bl2.child_item_id,
        bl2.process_seq,
        (e.child_qty * bl2.qty_per / (1 - bl2.scrap_pct / 100.0)),
        e.depth + 1,
        e.path || e.child_item_id,
        (bl2.child_item_id = ANY(e.path))
    FROM exploded e
    JOIN bom_headers bh2 ON bh2.item_id = e.child_item_id AND bh2.is_active = true
    JOIN bom_lines bl2 ON bl2.bom_header_id = bh2.id
    WHERE NOT e.cycle_detected AND e.depth < 50
)
SELECT parent_item_id, child_item_id, process_seq, child_qty, depth, cycle_detected
FROM exploded ORDER BY depth, parent_item_id
```

**The anchor** finds the root item's direct BOM lines — this is "level 1," seeded from `$1`
(root item id) and `$2` (the plan's quantity). **The recursive term** joins `exploded`'s own
output (`e`) back against `bom_lines` to find the next level down, and Postgres re-runs this
term against whatever the *previous* iteration produced, over and over, until an iteration
produces zero new rows — then stops automatically. `UNION ALL` (not `UNION`) stacks every
level's rows together into one final result — every BOM line reachable from the root, in one
query, one round trip.

### 1c. Why `child_qty` is computed *in SQL*, not in Go

`child_qty` carries the running multiplication through the recursion:
`e.child_qty * bl2.qty_per / (1 - scrap)`. Each level's qty becomes the next level's starting
point — the exact multiplication Day 2's Go recursion did node-by-node
(`childQty := qty * line.qtyPer`), just computed by Postgres instead. This is what makes the
Go side afterward a plain loop, not another recursion: every edge in the result already
knows its own final quantity, no further computation needed to reconstruct it.

### 1d. Cycle detection — `path`, `cycle_detected`, and why it's not just a filter

Two things thread through the recursion: `path` (every item id visited on *this row's own*
lineage) and `cycle_detected` (`bl2.child_item_id = ANY(e.path)` — is the child about to be
added already an ancestor of itself?).

**Why not just silently filter cycles out** (`WHERE NOT (child = ANY(path))` in the
recursive term, dropping cyclic rows entirely)? Because FR-3.4 requires *rejecting* a cyclic
BOM before any writes happen — not silently truncating it and proceeding as if the tree were
smaller than it really is. Silently filtering would make bad data look like a valid,
if-smaller, explosion. Flagging it instead (row still appears, `cycle_detected = true`, but
`WHERE NOT e.cycle_detected` stops recursion *past* it) lets Go check every row afterward and
raise `ErrCycle` — same explicit rejection Day 2's Go path-set gave you, expressed in SQL.

**Same cycle-vs-diamond semantics as Day 2's Go `map[int64]bool`, different mechanism.**
Day 2: a global-looking but path-scoped map, added on entry, `defer delete` on return, so a
diamond (same item via two sibling branches) is legal — by the time branch B reaches it,
branch A already removed it. Here: `path` is **per-row**, computed fresh for each
occurrence's own lineage (`e.path || e.child_item_id`), not a single shared structure. Two
sibling branches reaching the same item produce two *separate* rows, each with its own path,
neither containing the other — a legal diamond, naturally, with no explicit "remove on
return" needed because there was never a single shared mutable structure to remove from.
A true cycle means the child equals something in *that specific row's own* path — caught
regardless of which branch found it.

**The `depth < 50` cap** is a backstop, not the primary defense — insurance in case the flag
logic ever has a gap, not something that should ever actually trigger in correct code.

### 1e. Why the ROOT item's `bom_header_id` needs a separate lookup

The tree query only returns *edges* — a root with zero BOM lines produces zero rows, so its
own `bom_header_id` never appears anywhere in the result. `loadBomHeadersBatch` handles this
correctly by construction: it's queried for *every* item that will need a production order
(root included), and simply has no entry for an item with no active header — same "absent
from map" signal Go already uses elsewhere for "doesn't exist." Worth tracing this edge case
by hand once: a plan for a leaf make item (no sub-assemblies at all) — zero tree edges, one
production order, still correct.

### 1f. The other two batches — items and routing steps

`loadItemsBatch` and `loadRoutingStepsBatch` are plain `WHERE id = ANY($1)` /
`WHERE item_id = ANY($1)` queries — not recursive, just Day 2's per-node `loadItem` and
`routingSteps` calls collapsed into one query each, given the *full* set of item ids the
tree query already revealed. This is the "batch the aggregate/lookup queries" half of the
optimization — separate from the CTE, but the same underlying idea: one query for many
things beats one query per thing.

## 2. `explode_optimized.go` — same shape as Day 2, fed from memory

`explodeOptimized` does four things *before* touching any node: call `loadTree` once, collect
every distinct item id into one batch item-lookup, determine which items need production
orders (root + every child that turns out to be `make`, only knowable *after* the item batch
returns), then batch-load BOM headers and routing steps for exactly that set. Only after all
four upfront loads does `walkNode` run — and `walkNode` is structurally identical to Day 2's
`explode()`: same insert calls, same `process_seq`→work-order mapping, same recursion into
`make` children and accumulation of `buy` leaves into `buyReqs`. The only difference,
everywhere: `bomHeaders[itemID]` and `edgesByParent[itemID]` are map reads, not queries.
**No cycle check inside `walkNode`** — that already happened once, upfront, in `loadTree`.

## 3. `net_optimized.go` — one grouped query instead of N

Same formula, same lot-sizing, as Day 2's `net()`. The only change:
`SELECT item_id, SUM(qty) FROM inventory_movements WHERE item_id = ANY($1) GROUP BY item_id`
replaces the per-buy-item `SELECT SUM(qty) WHERE item_id = $1` loop — one round trip for every
buy item's on-hand total, regardless of how many there are.

## 4. Why both versions are kept, not one replacing the other

`Explode` (Day 2) and `ExplodeOptimized` (Day 3) both exist, both callable, both tested for
identical output before either number was trusted. This is deliberate, not an oversight:
it's what let `cmd/benchmrp -mode naive|optimized` produce a genuine, reproducible
before/after on the *same* codebase — and it's what an interviewer can be shown directly:
"here's the naive path, here's the optimized path, here's proof they agree." A rewrite that
silently replaced the naive version would have nothing to measure *against*.

## Self-check — answer cold, then compare

1. What does `UNION ALL` do here, and why not plain `UNION`?
2. Where is `child_qty`'s multiplication computed — SQL or Go — and why does that matter for
   what the Go code looks like afterward?
3. Why does the recursive term flag `cycle_detected` instead of just filtering cyclic rows
   out of the result entirely? Which FR requirement does that satisfy?
4. Why does `path` being per-row (not a single shared structure) allow a diamond but reject
   a true cycle — walk through both cases concretely.
5. What edge case does a root item with zero BOM lines expose, and how does
   `loadBomHeadersBatch` handle it correctly without special-casing?
6. Why is there no cycle check inside `walkNode`, when Day 2's `explode()` had one at the
   top of every call?
7. Name the four things `explodeOptimized` loads *before* touching a single node, and why
   the order among them isn't arbitrary (one depends on another's result).
8. Why keep `Explode` and `ExplodeOptimized` both in the codebase instead of deleting the
   naive one once the optimized one works?
