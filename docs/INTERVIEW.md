# Interview script — mrp-go

Verbatim answers. Read them, say them out loud, adjust the wording to sound like you.
Every number here is real and reproducible from `BENCHMARKS.md`.

---

## Q: "Tell me about this project." / "Walk me through your resume."

> "I work on a Salesforce-based manufacturing MRP product professionally — that's a real
> commercial system used by manufacturers to plan production. But the platform kept me
> mostly in Apex and declarative config, so I rebuilt the core planning engine myself in Go
> and Postgres to go deeper on backend engineering.
>
> The heart of MRP is BOM explosion. You take a production plan — say, build 100 lamps by
> the 15th — and walk the bill-of-materials tree to figure out every sub-assembly you need
> to build and every raw part you need to buy, in what quantities, by when.
>
> My first version was the obvious one: recursively walk the tree in Go, querying each
> node's children as I reached it. It was correct, but a bulk run across 500 production
> plans took almost 26 minutes.
>
> When I profiled it, the bottleneck wasn't computation — it was thousands of sequential
> database round trips. Classic N+1, in three separate places. So I rewrote the traversal
> as a single recursive CTE in Postgres and batched the other two lookups into set-based
> queries. Same output — I verified it row-for-row against the old implementation — but the
> bulk run dropped to a minute and a half. About 17x faster. Per plan that's 3.1 seconds
> down to 185 milliseconds, which is the difference between an overnight batch job and a
> planner re-planning interactively."

**Then stop talking.**

---

## THE MAIN EVENT: "Explain the performance work in detail."

*This is the deep-dive version — 5 minutes, ideally at a whiteboard. Draw the tree first.*

```
        Table Lamp  (make)          <- "build 100 of these"
        /     |        \
   Base(make) Shade    Bulb         <- level 1
    /    \    (buy)    (buy)
 Plate  Ballast
 (buy)   (buy)
```

> "Let me draw the shape of the problem first, because the fix follows directly from it.
>
> This is a bill of materials. A lamp is made of a base, a shade, and a bulb. The base is
> itself manufactured, so it has its own recipe — a plate and a ballast. Items I *make*
> recurse further; items I *buy* are leaves. Each edge has a quantity-per, so if I'm building
> 100 lamps and each needs 2 bulbs, that's 200 bulbs. That multiplication cascading down the
> tree is the explosion.
>
> **Version one.** I wrote the natural recursive function in Go. Visit a node, query its BOM
> lines, loop over the children, recurse into the ones that are make items. Textbook tree
> recursion, and it was correct.
>
> But look at what it does against the database." *(point at each node in turn)* "Query.
> Query. Query. Query. One round trip per node. Then for each node I also queried its routing
> steps and its item record — so really three queries per node. And after the tree walk,
> netting queried on-hand stock once per buy item. Three separate N+1 patterns.
>
> Across 500 plans that's roughly 13,000 nodes. The bulk run took 25 minutes and 55 seconds.
>
> **The diagnosis.** The thing that surprised me is that this is not a CPU problem. Go's own
> work per node — building the query string, deserializing the result — is microseconds.
> Essentially the entire 26 minutes was the goroutine *blocked*, waiting on a socket.
> It's I/O-bound, not compute-bound. So making the Go code faster was never going to help.
> I needed to remove round trips.
>
> **The thing I considered and rejected.** My first instinct was concurrency — fire the
> queries in parallel with goroutines. But that doesn't work across levels, because it's a
> real dependency chain: I can't query a node's children until its parent's query returns.
> Level 2 doesn't exist until level 1 comes back.
>
> I *could* parallelize siblings within one level, which would collapse 13,000 sequential
> queries into about 5 waves. That's genuinely faster. But it's still paying network latency
> per wave, and it adds concurrency complexity to what should be a simple traversal.
>
> **Version two.** Instead I moved the recursion into Postgres as a recursive CTE. The anchor
> selects the root's direct BOM lines; the recursive term joins the CTE's own output back
> against bom_lines to get the next level, and Postgres iterates until nothing new comes back.
> I carry the running quantity through the recursion so the multiplication happens in SQL, and
> an array of visited item IDs so I can detect cycles.
>
> The key insight is this: **the CTE has exactly the same dependency chain.** Postgres also
> can't compute level 3 before level 2 — recursion is recursion. What changed isn't the number
> of sequential steps, it's the *cost of each step*. Inside Postgres, going from one level to
> the next is an in-memory join against data it already has loaded — nanoseconds. From Go, it
> was a network round trip — milliseconds. Same number of steps, three orders of magnitude
> cheaper each.
>
> Then I batched the other two N+1s the same way: one query loads every item in the tree
> instead of one per node, one query loads every routing, and netting became a single
> `SUM ... GROUP BY item_id` for all buy items at once instead of one query each.
>
> **The result.** 25 minutes 55 seconds down to 1 minute 32 seconds. About 17x. Per plan,
> 3.1 seconds to 185 milliseconds.
>
> **How I know it's still right.** I kept both implementations rather than replacing one —
> they're `Explode` and `ExplodeOptimized` — seeded deterministically, ran both against
> identical data, and compared every output table row-for-row. Identical. That was the gate
> before I trusted the timing at all, because it's very easy to 'optimize' by accidentally
> dropping rows.
>
> **What's still slow.** The remaining 185 milliseconds is now dominated by inserting rows one
> at a time. I left that unbatched on purpose so the benchmark changed exactly one variable.
> Bulk insert is the obvious next win, then moving the whole thing to an async worker so the
> request doesn't block."

**If they only remember one line, make it this one:**
> "Concurrency can only parallelize independent work. It can't make an unavoidable sequential
> step cheaper — moving it into the database can."

---

## Q: "Was this at your job?" / "Was this in production?" / "How many users?"

> "The Salesforce product is my day job — that's the real commercial system. This Go engine
> is my own rebuild of its core planning logic. I built it because I wanted to go deep on
> backend systems engineering — recursion, query optimization, transactions — which the
> Salesforce platform doesn't really let you do. So it's not serving production traffic.
> The engineering and the numbers are mine, measured on my own code."

*Say this early and without hesitation. Volunteering it reads as integrity; getting caught
hiding it ends the interview.*

---

## Q: "Why was the naive version slow? Was Go the bottleneck?"

> "No, it was entirely I/O-bound, not CPU-bound. Go's own work per node was microseconds —
> building the query, deserializing the result. Almost all of the 26 minutes was the
> goroutine sitting idle, waiting on a response from Postgres over a socket.
>
> Across 500 plans there were roughly 13,000 BOM nodes, and each one paid a round trip. Then
> netting added another round trip per buy item. Rewriting it in a faster language wouldn't
> have helped at all — the fix had to remove round trips, not speed up computation."

---

## Q: "Why couldn't you just parallelize those queries with goroutines?"

> "Across tree levels you can't, because it's a genuine dependency chain — you don't know a
> node's children until its parent's query comes back. Level 2's queries literally don't
> exist until level 1 returns.
>
> You *can* parallelize siblings within a single level, since those are independent. That
> would turn N round trips into depth-many waves — maybe 5 waves instead of 13,000 queries.
> That's a real optimization and I considered it.
>
> But the CTE beats it, because it removes the round trips entirely instead of just
> overlapping them. Postgres does every level of the recursion in-process, in memory —
> that's nanoseconds per step instead of milliseconds per network hop. Concurrency can only
> parallelize independent work; it can't make the unavoidable sequential steps cheaper.
> Moving the traversal into the database does."

---

## Q: "Walk me through the recursive CTE."

> "It's two queries unioned together. The anchor selects the finished product's direct BOM
> lines — level one. The recursive term joins the CTE's own output back against bom_lines to
> get the next level down, and Postgres repeats that automatically until an iteration
> produces no new rows.
>
> I carry two extra things through the recursion. First, a running quantity — parent quantity
> times qty-per — so the explosion math happens in SQL rather than in Go. Second, an array of
> the item IDs already visited on that row's path, which is how I detect cycles.
>
> So one round trip gives me the entire tree, already depth-ordered, with quantities
> multiplied down. The Go side just loops over a flat result and writes the orders."

---

## Q: "How do you detect a cycle in a recursive CTE? Doesn't it loop forever?"

> "A plain one would — it'd keep finding 'new' rows until Postgres hit a resource limit.
>
> I thread an array of visited item IDs through each row of the recursion. In the recursive
> term I check whether the child I'm about to add already appears in that row's own ancestry
> path, and flag it if so. I also have a hard depth cap of 50 as a backstop in case the flag
> logic ever has a hole.
>
> The important part is that it's a *per-row* path, not a global visited set. That
> distinction matters: if the same component is used by two different sub-assemblies, that's
> a diamond — perfectly legal, two independent paths, neither contains the other. A real
> cycle is when an item appears in its own ancestry. A global visited set would wrongly
> reject the diamond.
>
> When a cycle is detected, the Go side returns an error before writing anything, and the
> transaction rolls back. A bad BOM produces zero rows, not a half-built order tree."

---

## Q: "How did you know the optimized version was still correct?"

> "I kept both implementations in the codebase rather than replacing one with the other —
> they're `Explode` and `ExplodeOptimized`. Then I seeded deterministically, ran both against
> the same data, and compared the output: production orders, work orders, component
> requirements, purchase requests. Identical counts on every plan I tested.
>
> That was the gate before I trusted any timing number. A faster-but-wrong version is
> worthless, and it's very easy to 'optimize' by accidentally dropping rows."

---

## Q: "What's still slow? Where would you go next?"

> "The remaining 185 milliseconds per plan is now dominated by individual row inserts. I left
> those unbatched deliberately, so the benchmark isolated exactly one variable — the query
> pattern — rather than changing two things at once and not knowing which one helped.
>
> Next steps in order: bulk-insert the orders using COPY or multi-row inserts, then move the
> whole run onto an async worker so the HTTP request returns immediately and streams progress
> instead of blocking for 90 seconds. There's also within-plan branch parallelism available if
> I ever needed it, but I'd measure before adding concurrency."

---

## Q: "What hardware? What data? How did you measure?"

> "500 production plans, a 50,000-item catalog, and a 2-million-row inventory ledger.
> Postgres 16 in Docker on my laptop, over a localhost socket. I wrote a small CLI that pulls
> every draft plan and explodes them one after another, timing the whole run.
>
> Worth noting that localhost is the *best* case for the naive version — a real deployment
> paying half a millisecond to two milliseconds of network latency per round trip would make
> the gap significantly wider, because the naive cost scales with round-trip count and the
> CTE's doesn't."

---

## Q: "Tell me about your data model." / "Why these tables?"

> "Two decisions I'd call out.
>
> First, bills of materials are a header plus lines, not one flat parent-child table. The
> header is 'this is one recipe for this product'; the lines are the ingredients. That's what
> lets one product have multiple recipes — a standard build and a budget build — without
> duplicating recipe-level data onto every component row. It also matches how the real
> commercial system models it.
>
> Second, inventory is an append-only ledger. Stock on hand is a SUM over movement rows, not
> a stored balance column."

---

## Q: "Why not just store a stock balance? Isn't SUM slow?"

> "It is slower to read, and that's a real trade-off I took deliberately.
>
> A mutable balance column has two problems. You lose the audit trail — you can't answer *why*
> stock is what it is, which matters a lot in manufacturing for traceability. And every
> concurrent write contends on the same row.
>
> The ledger gives me full history and no write contention. The read cost is fixable — a
> covering index, or a materialized snapshot table refreshed on write. I'd rather optimize a
> correct model than debug a fast wrong one. That indexing work is the next measured piece I
> have planned, and I'll benchmark it the same way."

---

## Q: "What's `process_seq`? Why does it matter?"

> "Each BOM line records which routing step consumes that component. So the component
> requirement doesn't attach to the production order generally — it attaches to the specific
> work order for the step that uses it.
>
> That matters because a component isn't needed when the order starts, it's needed when *its*
> step starts. If assembly is step 3 of a 5-day build, the parts for step 3 don't need to
> arrive on day one. Without that field you'd either order everything too early and hold
> unnecessary inventory, or schedule it wrong."

---

## Q: "What did you simplify versus the real system?"

> "Several things, deliberately. The real system supports make-to-stock and make-to-order per
> item; mine assumes everything is make-to-order, where each plan explodes independently with
> no netting shared across plans. It also has per-line effectivity dates, co-product and
> by-product tracking, and the ability to pin a specific variant recipe of a child component.
> I left all of that out.
>
> The simplification I'd flag most is the make-to-order assumption, because supporting both
> would require genuinely different netting logic — you'd have to aggregate demand across all
> plans instead of per plan, and you'd need low-level coding to process each shared component
> once at its deepest level. That's a real algorithmic difference, not just a missing field."

---

## Q: "Tell me about a bug you found." / "What went wrong?"

> "I caught a sign error in my own spec. I'd written the netting formula as
> net = gross − on_hand − safety_stock. That's wrong.
>
> Safety stock is a reserve you're not supposed to consume. So the stock actually *available*
> to meet demand is on_hand minus safety stock, which means net = gross − on_hand *plus*
> safety stock. With 50 on hand, 10 safety stock, and 200 demanded: available is 40, so you
> need 160. My original formula gave 140 — it would have quietly under-ordered and eaten into
> the buffer every single time.
>
> I caught it by deriving the formula from what safety stock is actually *for*, rather than
> transcribing what I'd written down. Fixed it in the code and the spec."

---

## Q: "Why build this at all? Why not use an existing MRP system?"

> "For a learning project, writing the traversal and doing the tuning myself was the entire
> point. In a commercial setting I'd absolutely evaluate existing systems first — MRP is a
> well-solved problem, and rebuilding it is only justified by a genuine domain mismatch or by
> learning goals. Here it was learning goals, and I'd say that honestly."

---

## Q: "This is mostly CRUD with one clever query, isn't it?"

> "The CRUD parts are the boring 20% and I won't defend them as interesting. The substance is
> the explosion algorithm, cycle detection, the transaction boundary that guarantees a bad BOM
> leaves zero partial rows, and the measured performance work. That's what I'd want to talk
> about."

---

## When you don't know something

> "I don't know off the top of my head. I'd measure it before guessing."

*This is a strong answer for you specifically, because measuring-before-guessing is a habit
you can actually demonstrate — every number in this project was measured, and you built both
implementations rather than assuming which was faster.*

---

## Questions to ask them

- "How do you handle long-running background work here — a job queue, worker pool, something
  managed?"
- "What's your process when someone proposes a schema change to a core table?"
- "Is performance work mostly reactive when something breaks, or do you measure proactively?"

---

## Before any interview

Re-read `BENCHMARKS.md` so the numbers are exact. "About 26 minutes down to about a minute
and a half, roughly 17 times" is right. Approximate is fine. **Wrong is fatal.**

Never attach the ¥20M revenue, 35% planning reduction, or 99.9% uptime figures to this
project. Those belong to the real product.
