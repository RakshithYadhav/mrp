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

### 1b-2. Reading the CTE when it doesn't click (added later, on re-reading `tree.go`)

Section 1b describes the query's *parts*. This section is the mental model for its *execution* —
written because the parts-list description isn't enough to make the thing readable cold.

**The one idea: in Go, recursion state lives in function arguments; in SQL, it lives in columns
of a row.** Day 2's `explode(item, qty, parentOrderId)` plus `e.path` is four pieces of state on
the call stack. The CTE carries the identical four pieces as columns:

| Day 2 Go recursion | CTE column |
| --- | --- |
| `item.id` argument | `child_item_id` |
| `qty` argument | `child_qty` |
| depth of the call stack | `depth` |
| `e.path` map | `path` array |

A row in `exploded` **is** a pending call frame. Everything else is mechanics.

**It is a loop, not a call stack.** Postgres keeps a *working table* and runs:

1. Run the anchor. Its rows → result, and → working table.
2. Run the recursive term with `exploded` bound to **only the working table** (the previous
   iteration's rows — *not* everything accumulated so far). Its rows → appended to result, and
   → become the new working table.
3. Repeat 2 until an iteration produces zero rows. Stop.

So `FROM exploded e` does not mean "the whole result so far," it means "the rows I just made."
Each iteration processes one entire BOM *level* at once — breadth-first, whereas Day 2's Go
version was depth-first. Same final set of edges, different visitation order; nothing downstream
depends on the order because `walkNode` re-walks the tree itself from `edgesByParent`.

**Worked trace.** Items `1 BIKE → 2 FRAME (×1, seq 10), 3 WHEEL (×2, seq 20)`;
`2 FRAME → 4 TUBE (×4, seq 10, 20% scrap)`; `3 WHEEL → 5 SPOKE (×32)`; items 4 and 5 are buy
items with no BOM header. Called with `$1 = 1`, `$2 = 10`.

Anchor (`WHERE bh.item_id = $1` seeds level 1 from the root's own header):

| parent | child | qty | depth | path | cycle |
| --- | --- | --- | --- | --- | --- |
| 1 | 2 | 10 (`10×1`) | 1 | `{1}` | f |
| 1 | 3 | 20 (`10×2`) | 1 | `{1}` | f |

Iteration 1 — `e` = those two rows only. For each, join `bom_headers bh2 ON bh2.item_id =
e.child_item_id` ("does the child I'm pointing at have its own BOM?"), then join its lines:

| parent | child | qty | depth | path | cycle |
| --- | --- | --- | --- | --- | --- |
| 2 | 4 | 50 (`10×4/0.8`) | 2 | `{1,2}` | f |
| 3 | 5 | 640 (`20×32`) | 2 | `{1,3}` | f |

`e.child_item_id AS parent_item_id` is the line doing the handoff — the child of the previous
level becomes the parent of this one, exactly like `e.explode(ctx, child, childQty, orderId)`
passing the child as the next subject.

Iteration 2 — `e` = the two rows above. Items 4 and 5 have no `bom_headers` row, so the inner
join drops both. **Zero rows → recursion halts.** There is no base case to write: "the join
found nothing" *is* the base case. Final `SELECT` returns all 4 rows `UNION ALL`'d together.

**The individually cryptic pieces:**

- `ARRAY[bh.item_id]` / `e.path || e.child_item_id` — `||` is array append. Each row builds its
  own ancestor list by copying its parent's and appending one id. Per-row copies, never one
  shared structure — which is exactly why diamonds are legal with no `defer delete` equivalent
  (see 1d).
- `bl2.child_item_id = ANY(e.path)` — "is the child I'm about to add already one of my own
  ancestors?" The same test as `if e.path[item.id]` at the top of Day 2's `explode()`.
- `WHERE NOT e.cycle_detected` — filters the *input* rows of the next iteration, not the output.
  A flagged row still lands in the result (so Go can see it and raise `ErrCycle`), but nothing
  expands *past* it.
- `/ (1 - scrap_pct/100.0)` — inflate, don't discount: at 20% scrap you must start 50 to yield
  40. Day 2 guarded this with `if scrapPct > 0`; the CTE always divides, which is equivalent
  because dividing by 1 is a no-op.
- `$2::numeric` — pgx sends `rootQty` as float8. The cast keeps the whole multiplication chain
  in `numeric`, matching the column types, instead of silently degrading to double precision.
- **The output is *edges*, not nodes.** A root with zero BOM lines returns zero rows — which is
  the whole reason `loadBomHeadersBatch` exists as a separate lookup (see 1e).

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
things beats one query per thing. `loadItemsBatch`'s `item_type` column is what §2's
`walkNode` branches on for make (production order) vs. buy (purchase request/`buyReqs`).

**Why Day 2 structurally couldn't batch.** Day 2 discovers the tree by querying it — each
node's BOM-lines query is what *reveals* its children, so the next query's input doesn't
exist until the current one returns. Nothing to batch: you can't batch queries whose targets
you haven't discovered yet. `loadTree`'s CTE breaks that dependency by discovering the whole
tree's structure in one SQL round trip; only once Go holds the full id set upfront does
batching the item/BOM-header/routing-step lookups become possible at all.

### 1g. `defer rows.Close()` in `loadTree` — why it's there when `Next()` already closes on exhaustion

`e.tx.Query` reserves the transaction's connection for as long as the result set stays open
— no other statement can run on it until the rows are drained or closed.

**What "connection" means concretely.** A real TCP (or Unix domain socket) connection to one
PostgreSQL backend process — Postgres is one OS process per connection, not
thread-per-connection or a shared multiplexer. `pgx.Conn` is the Go-side wrapper speaking the
wire protocol over that one socket; `pgxpool.Pool` holds a set of them. `BeginTx` checks out
exactly one `pgx.Conn` and pins the *entire transaction* to it — every statement on `e.tx`
runs on that same socket until commit/rollback returns it to the pool. An open result set
blocks the next query for a protocol reason, not a library-imposed one: the wire protocol is
one synchronous request/response stream per socket, so the client must finish reading the
current query's response — or explicitly tell the server to abandon it, which is what
`Close()` does on an undrained set — before it can send the next query frame down that same
connection.

### 1g-2. How a TCP connection is actually established (background, skip if solid)

Building it up in layers, from the physical wire to the thing pgx actually holds onto:

**Layer 2 — wire to switch.** The PC's NIC has a wire into a switch port. A switch only
understands Ethernet frames addressed by MAC address — it learns which MAC lives on which
port by watching traffic, then forwards each frame out just that port. This alone delivers a
frame to another device on the same local network. No "connection" exists yet at this layer —
it's frame-by-frame delivery, nothing more.

**Layer 3 — routing, if the destination is off-network.** The PC sends to its default gateway
(a router), addressed with the destination's IP. Routers forward packet by packet based on IP
address, one independent decision per packet — no memory that this packet is "part of" the
same conversation as the last one. In this project specifically, `DATABASE_URL` points at
`localhost:5433` and Postgres runs in a Docker container on the same machine, so this hop is
really Docker Desktop's virtual networking doing a NAT/port-forward from host port 5433 into
the container's Postgres process on 5432 — no physical switch involved, but the same idea:
something forwards packets by address, statelessly.

**Layer 4 — the TCP three-way handshake, where "connection" is actually created.** Layers 2–3
only move individual packets around; no connection concept exists there. TCP is what creates
one, and it's a purely logical agreement between the two OS kernels, not a reserved wire:
client sends **SYN** (starting sequence number) → server replies **SYN-ACK** (its own sequence
number, plus acknowledging the client's) → client sends **ACK**. After those three packets,
both kernels have allocated a socket data structure — sequence numbers, window size, buffers —
and *that bookkeeping is the connection*, identified only by the 4-tuple (source IP, source
port, destination IP, destination port). No wire is reserved for it.

**The mental-model correction that matters:** switches and routers never hold or remember "the
connection" — each forwards packets independently and statelessly. The *only* two places that
know a TCP connection exists are the client's kernel and the server's kernel. This is why pgx
does the TCP handshake once, layers Postgres's own wire-protocol auth handshake on top of that
same socket, and `pgxpool` then keeps that live, authenticated socket around and reuses it for
many queries — avoiding both handshakes on every call.

`rows.Next()` returning `false` at normal exhaustion closes it automatically, so on the happy path
`Close()` is a no-op. What it actually covers is the early `return nil, err` inside the
`rows.Scan` loop (line 81): that bails out with rows still open. Without the `defer`, the
transaction's connection stays marked busy, and the next statement on that same `tx` — the
`ROLLBACK` the caller is about to issue on this exact error path — fails too. With a
`pgxpool`-managed connection generally (not this specific `tx` case, but the same rule), an
unclosed result set can also mean the connection never gets released back to the pool. So:
`defer rows.Close()` immediately after the `err` check, always — cheap when unneeded, the
only thing covering every early return that gets added later inside the loop.

This is a different check from `rows.Err()` at line 85, and one doesn't replace the other.
`Close()` releases the connection; `rows.Err()` reports *why* iteration stopped — data
exhausted normally, or the query died mid-stream (server error, killed connection). Skipping
`rows.Err()` would silently turn a failed query into an empty result set, which here means an
explosion that quietly produces zero component requirements instead of an error.

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

## 5. Open issue found while re-reading `tree.go` — diamonds and `edgesByParent`

Not yet fixed as of this writing; recorded here so it isn't rediscovered from scratch.

`explodeOptimized` builds `edgesByParent` keyed on `parentItemID` alone. But the CTE emits one
row **per occurrence**, not per distinct parent — that is the very property 1d relies on for
diamond handling. So if item X is reached under two different branches, X's own child edges
appear *twice*, with a different `child_qty` each time (each derived from its own branch's
quantity). `edgesByParent[X]` then holds both, and `walkNode(X, …)` — itself called once per
occurrence — loops over *both* on every call, inserting duplicate `component_requirements`
carrying the other occurrence's quantity, and recursing into each child twice.

Day 2's naive path does not have this: it recomputes `childQty := qty * line.qtyPer` from the
live `qty` argument on each call, so each occurrence gets its own correct quantity.

Two candidate fixes: key the map on the occurrence rather than the parent item id, or drop
`child_qty` from the walk entirely — carry `qty_per`/`scrap_pct` on the edge and recompute from
the `qty` argument the way Day 2 does, which keeps the CTE's multiplication for cost analysis
but makes the Go walk authoritative for what gets written. Either way this needs a v1-vs-v2
equivalence test on a hand-built diamond BOM first — the claim in §4 that both paths were
"tested for identical output" evidently didn't cover a diamond.

**The general lesson, worth keeping past this bug:** moving a computation into the recursive CTE
changes what a result row *means*. Day 2's rows were "a BOM line" (one per parent/child pair);
the CTE's rows are "a BOM line **at a position in the tree**" (one per path to that pair). Any
Go code that groups CTE output has to group on the new identity, not the old one.

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
9. When the recursive term says `FROM exploded e`, which rows is `e` actually bound to on the
   third iteration — all rows produced so far, or only the second iteration's? What does the
   wrong answer lead you to believe about the query's cost?
10. Where is the base case of this recursion written? (Trick question — answer what actually
    stops it.)
11. Day 2's Go walk is depth-first; the CTE is breadth-first. Does anything downstream depend
    on that difference, and why or why not?
12. Which line of the recursive term corresponds to `e.explode(ctx, child, childQty, orderId)`
    in Day 2 — i.e. where does the child become the next level's subject?
13. Scrap is 20%. Explain why the formula divides by `(1 - 0.20)` rather than multiplying by
    `1.20`, and give the number for a 40-unit requirement under each.
14. What does a single row of the CTE's output *mean*, precisely — and how does that differ
    from what a row of Day 2's `bomLines()` meant? Use that difference to explain why keying
    `edgesByParent` on `parentItemID` is wrong for a diamond BOM.
15. Why is `$2` cast with `::numeric` when pgx already knows the Go type of the argument?
16. `loadTree` has both `defer rows.Close()` right after the error check and a `rows.Err()`
    check after the loop. What does each one actually guard against, and what would you
    observe go wrong if you deleted just one of them (not both)?
