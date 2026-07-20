# MRP Fundamentals & BOM Traversal — read before deciding AD-1/AD-2

Two purposes in one file: Part 1 recaps the core MRP algorithm explained early in this
project (never written down until now — don't lose it to context drift). Part 2 covers the
specific new concepts AD-1 and AD-2 actually require that haven't come up yet.

## Part 1 — What MRP is actually computing

**The problem MRP solves.** Ordering finished goods is easy — a customer tells you what and
when. The hard part is *dependent demand*: nobody orders spokes from you, but every bike you
promised implies exactly 72 of them, needed weeks before the ship date, when wheel assembly
starts. MRP's insight: don't forecast what you can calculate.

**The three inputs.** A production plan (item, quantity, due date). A Bill of Materials —
the recursive parent→child structure with a *quantity-per* on each edge (this project:
`bom_headers` + `bom_lines`, `qty_per`, `process_seq`). Inventory & order status — on-hand
per warehouse, safety stock, make-vs-buy classification (`item_type`), lead time, lot-sizing
rule.

**The algorithm, four steps:**

1. **Explosion.** For each planned order, multiply through BOM lines to create gross
   requirements on child items, dated by when the consuming process step starts. This is
   FR-3 — what AD-1 is about: *how* you walk the tree to do this multiplication.
2. **Netting (gross-to-net).** Per item: `net = gross − on_hand − scheduled_receipts +
   safety_stock`. Only shortfalls generate new orders. This is FR-4 — what AD-2 is about:
   *when* in the process this subtraction happens.
3. **Lot sizing.** Round net requirements into practical order quantities — lot-for-lot
   (order exactly what's needed) or fixed-lot (round up to a multiple). Already modeled in
   the schema (`items.lot_size_rule`, `fixed_lot_size`).
4. **Lead-time offsetting (backward scheduling).** `release date = due date − lead time`,
   walked backwards through the routing, skipping non-working days. This is FR-5, for Day 4
   — not needed yet for AD-1/AD-2, but it's why `process_seq` matters: a component's due
   date comes from the specific step that consumes it, not the order as a whole.

**A worked example, concretely (redo this by hand once if it's gone fuzzy):** 100 bikes due
day 10. Bike = 1 frame + 2 wheels; wheel = 36 spokes. Wheel: on-hand 50, lead time 3 days.
Bike assembly lead time 2 days.

| Wheel | day 5 | day 8 | day 10 |
|---|---|---|---|
| Gross requirement | | 200 ← (100 bikes × 2, needed when bike assembly starts on day 8) | |
| On-hand | 50 | 50 | |
| Net requirement | | 150 | |
| Planned order release | 150 ← (8 − 3 day lead time) | | |

The wheel's release on day 5 becomes a **gross requirement of 150 × 36 = 5,400 spokes on
day 5**, and the cascade continues down the tree. That grid — the time-phased record — *is*
MRP; everything else (async jobs, indexing, scheduling) is engineering around it.

**Low-level coding** (a term, not a rule you need yet): in textbook MRP, an item appearing
at multiple levels across different products must be processed only once, at its *lowest*
level, after all demand for it has accumulated — otherwise you'd double-count or process it
before all its demand is known. **This project doesn't need it**, because of a specific
design choice already recorded in `docs/FRD.md` §7: this is *seiban* (pegged, make-to-order)
MRP — each plan's explosion is independent, no netting shared across plans — so an item is
only ever processed once per plan's own tree, never once globally across all demand. If
cross-plan netting were ever added as a stretch goal, low-level coding would become
necessary and this changes.

## Part 2 — What AD-1 and AD-2 actually need that's new

### Trees, briefly

The BOM is a tree: a root node (the plan's item), children (its direct components), and
their children, down to raw materials at the leaves. Vocabulary worth having: a node's
**depth** is how many levels down from the root it sits; a **leaf** has no children. The
seeder builds this up to 5 levels deep (`RAW → SUB level 1 → level 2 → level 3 → FG`).

### Recursion, briefly

A function that solves a problem by calling itself on a smaller version of the same
problem, with a **base case** that stops it (here: "this item has no BOM lines under it,
stop"). "Walking the tree in Go" (AD-1's Option B) means writing exactly this: a function
that, given an item, processes its direct `bom_lines`, then calls itself on each child item.

### Recursive SQL — `WITH RECURSIVE`, the actual new concept for AD-1

Postgres can do the same recursive walk *inside a single query*, without Go ever seeing the
intermediate steps. Shape: an **anchor** query (the starting rows) `UNION ALL`'d with a
**recursive term** that references the CTE's own output — Postgres re-runs the recursive
term against whatever the *previous* iteration produced, over and over, until an iteration
produces zero new rows, then stops automatically.

**Real example, run against this project's own seeded data** — exploding finished good
`FG-000001`'s BOM two levels deep:

```sql
WITH RECURSIVE exploded AS (
    -- anchor: level 1 — FG-000001's direct components
    SELECT bh.id AS bom_header_id, i.code AS item_code, bl.child_item_id, bl.qty_per, 1 AS depth
    FROM bom_headers bh
    JOIN items i ON i.id = bh.item_id
    JOIN bom_lines bl ON bl.bom_header_id = bh.id
    WHERE bh.id = 1
    UNION ALL
    -- recursive term: for each child found so far, find *its* children
    SELECT bh2.id, ci.code, bl2.child_item_id, bl2.qty_per, e.depth + 1
    FROM exploded e
    JOIN bom_headers bh2 ON bh2.item_id = e.child_item_id
    JOIN items ci ON ci.id = bh2.item_id
    JOIN bom_lines bl2 ON bl2.bom_header_id = bh2.id
)
SELECT depth, item_code, child_item_id, qty_per FROM exploded ORDER BY depth LIMIT 15;
```

Actual output from this project's dev database:
```
 depth | item_code  | child_item_id | qty_per
-------+------------+---------------+---------
     1 | FG-000001  |           918 |       9
     1 | FG-000001  |          2998 |      10
     1 | FG-000001  |          2701 |       8
     1 | FG-000001  |          2977 |       4
     1 | FG-000001  |          3508 |      10
     2 | SUB-000477 |           801 |       8
     2 | SUB-000477 |           116 |       5
     2 | SUB-000201 |           328 |      10
     2 | SUB-000477 |           417 |       1
     2 | SUB-000201 |          3042 |       9
     ...
```
Read this literally: `FG-000001` has 5 direct components at depth 1 (one of them is item
`918`, coded `SUB-000477` — you can see it show up as the *parent* at depth 2, meaning the
recursive term correctly found *its* children on the next iteration). One query, no loop in
application code, Postgres did every level of recursion internally.

**You can run this yourself** against your own dev database:
```sh
docker exec mrp-go-db-1 psql -U mrp -d mrp -c "<paste the query above>"
```
Try changing `WHERE bh.id = 1` to explore a different finished good, or remove the `LIMIT`
to see the full tree. Seeing it actually run matters more than reading about it.

### What a CTE actually is, if that term is fuzzy

**CTE = Common Table Expression.** A way to name a temporary result set, defined right
before the main query with `WITH`, then queried like a table within that query — it isn't
a real table sitting in the database, it only exists for that one query's duration. "Common"
because it can be referenced more than once within the surrounding query; "table
expression" because it behaves like a table wherever it's used. A plain CTE is really just
a named, reusable subquery for readability. The special case is `WITH RECURSIVE`: a
recursive CTE is allowed to reference *itself* inside its own definition, which a plain
subquery can never do — that self-reference is exactly what makes the tree-walking
behavior possible: the recursive term says "take what the CTE has produced so far, find
the next level from it."

### Naive Go recursion vs the CTE, measured for real — and why Go isn't "slow"

Measured against this project's own seeded data (`FG-000001`'s tree, 23 BOM headers, 94
lines): 23 separate per-header queries over one connection took **17.994ms total**
(0.782ms average each); the single recursive CTE fetching the same data took **9.394ms**
in one round trip — roughly **1.9x faster on localhost**, and that gap would be
substantially larger against a real deployed database, where each network round trip costs
0.5–2ms of pure latency instead of localhost's ~0.05–0.1ms, and it *grows* with tree size
since the naive approach's round-trip count scales with the number of BOM headers while the
CTE's stays at exactly one.

**Why, mechanically — and this is not "Go is slow."** In each of the 23 round trips, Go's
own work (build the query, serialize it, deserialize the response) is microseconds —
genuinely negligible. Almost all 17.994ms was Go's goroutine *waiting*, blocked on a
response from a separate process (Postgres) over a network socket. Rewriting the identical
naive pattern in Rust or C would take almost exactly as long, because the bottleneck is
network round-trip latency, not computation speed — an entirely I/O-bound cost, not a
CPU-bound one.

**Why those round trips can't just be made concurrent.** Concurrency only helps when work
is independent — it cannot shorten a chain where each step needs the previous step's
result. You cannot query "what are `SUB-000477`'s children?" before you've *learned
`SUB-000477` exists* — and you only learn that from the result of its parent's query.
There's nothing valid to run concurrently *across* tree levels; the dependency is real, not
artificial. Concurrency *does* help *within* one level, once it's known (5 known siblings'
lookups are independent of each other, so they could run as 5 concurrent goroutines instead
of 5 sequential round trips) — turning "node-many sequential round trips" into
"depth-many sequential waves." But it can't go below depth-many, because depth is the
actual length of the dependency chain (the critical path), and no amount of concurrency
shortens a critical path — it only parallelizes what's independent within it.

**The sharper point this leads to:** the recursive CTE has *exactly the same* level-by-level
dependency internally — Postgres can't compute level 3 without level 2's results either,
recursion is recursion, that structural fact doesn't disappear. The CTE isn't escaping the
dependency chain. What it's escaping is **the cost of each link in that chain** — each hop
between levels is an in-memory join against data Postgres already has loaded, not a network
round trip to a separate process. The real lesson: concurrency parallelizes independent
work; it was never going to fix a cost that comes from paying network latency once per
unavoidable sequential step. Making each step cheap (in-memory instead of network-bound) is
what actually fixes it — which is exactly what moving the traversal into the database does.

### Why cycle detection needs an explicit guard here

A tree, by definition, has no cycles. But nothing *stops* someone from creating a BOM where
item A contains B and B (a few levels down) contains A again — a cycle, not a tree. A plain
`WITH RECURSIVE` query would keep finding "new" rows forever on a true cycle and either run
until Postgres hits a resource limit or hang. (This project's seeder never generates one —
see day-01-foundations.md §9, "by construction" — but *user-entered* BOM data isn't
guaranteed acyclic, which is exactly why FR-3.4 requires detecting and rejecting cycles
before any writes happen.) The guard is the same idea either way: track which items have
already been visited on the current path, and stop — refuse to recurse further — the moment
you'd revisit one. In Go this is a natural `map[int64]bool` "visited" set. In SQL, the
common pattern is carrying an array of visited IDs through the recursion and checking
`NOT (child_item_id = ANY(visited_path))` in the recursive term's `WHERE` clause.

### Why `bom_headers` + `bom_lines` instead of one flat table

The obvious denormalized alternative is a single table: `(parent_item_id, child_item_id,
qty_per)`. This project didn't pick that. Trade-offs:

**What splitting into header + line buys you:**
- **Versioning/alternates without duplicating it onto every row.** `bom_headers.name` lets
  one item have multiple named BOMs (e.g. "Standard" vs an alt routing), each with its own
  independent line set. A flat table would need a `bom_version` column repeated on *every*
  line just to distinguish them.
- **BOM-level metadata lives once.** `is_active` (and anything added later — effective date,
  approval status) describes the BOM as a whole. Flat-table would force duplicating it across
  every line for that BOM (inconsistent-state risk: some lines say active, some don't) or
  spinning up a second table for it anyway — which is header+line by another name.
- **Mirrors the source system** this project reverse-engineers (header object + line object
  in the commercial Salesforce package) — a real precedent, not just a schema preference.

**What it costs:** one extra join per level during traversal — `items → bom_headers (which
BOM?) → bom_lines`, visible in the recursive CTE above where the recursive term joins through
`bom_headers` before it can reach `bom_lines`. A flat table collapses that into a single join
per level (`WHERE parent_item_id = ANY(...)`), at the cost of losing versioning and header
metadata support outright.

Net: flat is simpler if a BOM is always exactly one unversioned thing. The moment "an item
can have more than one BOM" is a real requirement (already true here — schema supports named
BOMs per item), header+line stops being over-engineering and becomes the minimum structure
that can express it.

### N+1 queries and batching — the concept AD-2 turns on

Already covered concretely in `day-01-foundations.md` §7 (the `ListPlans` JOIN discussion)
— re-read that if it's fuzzy. The short version: one query that gets everything you need
beats one query per item in a loop, because each query is a network round trip, and round
trips dominate at scale even when each individual query is fast. This is the actual force
behind AD-2's naive-vs-optimized split: netting inline, once per BOM line as you walk the
tree, is N+1 against `inventory_movements`; netting in a batch (`WHERE item_id = ANY($1)`)
for every item that needs it is one query regardless of tree size.

## Reading list

- **PostgreSQL Tutorial's "PostgreSQL Recursive Query"** page (postgresqltutorial.com) —
  probably the best plain-language walkthrough, uses an org-chart example that maps
  directly onto BOM explosion. Read this before the official docs, not instead of them.
- PostgreSQL docs, **"WITH Queries (Common Table Expressions)"** — the official docs, search
  for the recursive-query section specifically. Denser but authoritative; the primary
  source for AD-1 once the tutorial version clicks.
- Search **"recursive CTE bill of materials example"** — several practical writeups beyond
  Postgres's own docs, usually with a parts-explosion example similar to this project's.
- Search **"recursive CTE cycle detection postgres"** for the visited-path array pattern
  mentioned above, if you want to see it written out in full SQL.
- Search **"N+1 query problem explained"** — one of the most commonly written-about backend
  problems (mostly in Rails/Django/GraphQL contexts, but the concept is identical here); any
  well-rated explainer works, the concept doesn't change across languages. Ties directly to
  AD-2.
- **Orlicky's Material Requirements Planning** (Ptak & Smith, 3rd ed.) and **Hopp &
  Spearman's Factory Physics**, ch. 3 — the MRP fundamentals reading from project kickoff,
  worth revisiting if it's drifted; Factory Physics specifically has the sharpest critique
  of MRP's real weaknesses (infinite capacity, lead-time fiction), which is what separates
  a staff-level answer from a textbook one.
- **Odoo's `mrp` module** or **ERPNext's manufacturing module** (both open source, on
  GitHub, Python) — reading a real BOM-explosion implementation in a different language is
  often more clarifying than any article.
- Re-read `docs/FRD.md` §10 (Acceptance Scenarios) — the Gherkin-style examples there
  (including the explicit cyclic-BOM scenario) are the test cases FR-3's implementation
  needs to satisfy, whichever traversal approach gets picked.

## Self-check — answer cold before the AD-1/AD-2 discussion resumes

1. In your own words: why doesn't this project need "low-level coding," and what specific
   design choice is that a consequence of?
2. In the worked wheel/spoke example, why does the wheel's *planned order release* land on
   day 5 and not day 8?
3. In the recursive CTE above, what does the anchor query produce, and what does the
   recursive term do differently on its second execution versus its first?
4. What concretely goes wrong if you run a `WITH RECURSIVE` query against a BOM that
   contains a genuine cycle, with no guard in place?
5. Why is netting inline during tree traversal naturally N+1, and what would the batched
   alternative's query actually look like?
6. Why does this project use `bom_headers` + `bom_lines` instead of one flat
   `(parent_item_id, child_item_id, qty_per)` table, and what requirement would have to
   disappear for the flat version to be the better choice?
