**Why Does Net() Exist**
- See once we expand / explode our bom we get a list of items that we need to buy for the final product. 
- Example 
    - Lets say we need to buy 100 screws
    - maybe 50 bolts
    - 10 lamp bases.
- Now this is the total required amount needed to create the orignal production plan. but if you think about you don't have to buy all the parts honestly thats a waste. because you might have your own inventory.
- so the actual amount you have to buy is.
    - **net = total - onhand**
- After we find the net, logically speaking we need to make a purchase order request for those items.
- So thats what net does.

**Algorithm For Net()**
- first the map of item to its qty consists of item id.
- get the actual item data.
    - Now for the item data. get the total quantity present currently - Compute the onhand
    - Calclate the the net = total - onhand + stocksafety.
    - if net <= 0 which means the on hand is enough that we dont need to create purchase requests.
    - if item as fixedLotSize then we have to round the net = ceil(net/fixed) * fixed.
    - Once the net is determined, create and insert purchase order requests.

---
*(Section below is Claude's summary, pasted in on request — not written from scratch by
Rakshith. Kept separate from the notes above, which are.)*

**Naive Go recursion — pros/cons**
- Pros: trivial cycle detection (`map[int64]bool` path set); fully unit-testable in
  isolation with in-memory fixtures, no DB needed for algorithm correctness; debuggable
  with normal breakpoints/stepping; naturally interleaves traversal with writing rows in
  one pass; typed Go errors are easy to construct and propagate; gives the honest
  unstaged baseline the naive→optimized story depends on.
- Cons: N+1 round trips — measured directly, 23 headers = 17.994ms vs the CTE's 9.394ms
  for the same data (~1.9x slower even on localhost's best-case latency); round trips are
  sequential across tree levels (can't know children before the parent query returns);
  forfeits the DB engine's own set-based execution; cost compounds multiplicatively at
  bulk scale (the 500-plan benchmark currently running is proof of this).

**Recursive CTE (`WITH RECURSIVE`) — pros/cons**
- Pros: one round trip regardless of tree size/depth, and the gap *widens* as trees grow;
  internal recursion steps cost nanoseconds-to-microseconds each vs. milliseconds per
  network hop; can compute running quantity + depth inline, handing Go a flat,
  depth-ordered result to just loop over; a genuinely non-trivial SQL skill to show off.
- Cons: cycle detection needs an explicit visited-path array in SQL
  (`NOT (child = ANY(path))`) — more error-prone than a Go map; much worse testability —
  correctness only verifiable by actually running it against a real/test DB; harder to
  debug (no stepping, just `EXPLAIN ANALYZE` and scratch `SELECT`s); the write side
  doesn't go away — Postgres can't cleanly self-reference-insert a new order tree in one
  statement, so Go still consumes the CTE's flat output and builds the hierarchy; locked
  into Postgres-specific SQL.

**The actual decision (ADR-0002):** not "pick one forever" — build both, in sequence.
Naive first (Day 2, real measured baseline), CTE second (Day 3, optimized, measured again
for a real before/after). Both sets of trade-offs are true at once; the point of building
both is defending *both* decisions with real numbers, not just the winning one.
