# Interview delivery playbook — mrp-go

`docs/RESUME.md` holds *what* the bullets claim and the evidence behind them. This file is
*how to deliver it out loud*: structure, pacing, the whiteboard sketch, depth control, and
the traps. Practice against this, not against reading the code.

---

## 1. The universal structure — use this for every technical story

Four beats, in this order, every time:

1. **Context** (1 sentence) — what the system is, in *domain* terms a non-expert follows.
2. **Problem** (1–2 sentences) — what was wrong, ideally with a number.
3. **Diagnosis** (1–2 sentences) — how you *found out* what was wrong. This is the beat
   most candidates skip, and it's the one that separates "I applied an optimization I read
   about" from "I engineer." Interviewers listen hardest here.
4. **Action + Result** (2–3 sentences) — what you changed, and the measured outcome.

Then **stop talking.** Silence invites them to steer toward what *they* care about, which
is where you score. Candidates who monologue for four minutes lose the room even when
every sentence is correct.

**Rule of thumb:** your first answer to any "tell me about X" should be ~60 seconds. If
they want more they will ask — and then you're answering their actual question instead of
guessing.

---

## 2. Depth control — have three versions of every story ready

Interviewers probe at different depths. Match them; don't dump your deepest version on a
surface question.

**30 seconds (they're scanning your background):**
> "I rebuilt the core planning engine of a manufacturing MRP system in Go and Postgres —
> the part that takes a production order and works out everything you need to build and buy.
> The interesting engineering was performance: I got a bulk planning run from ~26 minutes
> down to about 90 seconds."

**2 minutes (the default — this is the B2 opener in `RESUME.md`):**
Context → problem → diagnosis → action → result. Full narrative, then stop.

**10 minutes (they're genuinely interested, or it's a system-design round):**
Now you go into the recursive CTE mechanics, cycle detection, the correctness verification,
the trade-offs you rejected, what's still slow. Use the whiteboard (§3).

**The signal to go deeper:** they ask a *specific* follow-up ("how does the CTE handle
cycles?"). The signal to stay shallow: they nod and move to a different topic.

---

## 3. The whiteboard sketch — draw this, don't just talk

Almost every deep-dive goes better if you draw the BOM tree. Practice drawing this in
under 30 seconds:

```
        Table Lamp  (make)          <- the production plan: "build 100"
        /     |        \
   Base(make) Shade    Bulb         <- 1 level down
    /    \    (buy)    (buy)
 Plate  Ballast
 (buy)   (buy)
```

Then narrate over it:
- "Each edge has a quantity-per — 100 lamps × 2 bulbs each = 200 bulbs. That multiplication
  cascading down the tree *is* the explosion."
- "Make items recurse — they have their own recipe. Buy items are leaves; they become
  purchase demand."
- "The naive version queried at every node as it walked. The CTE gets the whole tree in one
  round trip."

**Why drawing wins:** it makes the N+1 problem *visually obvious* — you can literally point
at each node and say "one round trip, one round trip, one round trip." Far more persuasive
than the words alone.

---

## 4. Scripts by bullet

### B2 — near-real-time MRP (the strongest story)

Full opener + 6 follow-up Q&As are in **`docs/RESUME.md` → "Interview script for B2."**
That's your primary story — lead with it whenever they ask about backend, performance, Go,
or databases.

### G / B1 — data modeling & SQL

**Opener:**
> "The data model was the part I spent the most design time on, because in manufacturing the
> schema *is* the domain. Two decisions I'd call out. First, bills of materials are a header
> plus lines rather than one flat parent-child table — that's what lets one product have
> multiple recipes, a standard build and a budget build, without duplicating recipe-level
> data onto every component row. Second, inventory is an append-only ledger — stock on hand
> is a SUM over movements, never a stored balance column."

**The follow-up that always comes: "Why not just store a balance? Isn't SUM slow?"**
> "It is slower to read, and that's a real trade-off I took on purpose. A mutable balance
> column has two problems: you lose the audit trail — you can't answer *why* stock is what
> it is — and every concurrent write contends on one row. The ledger gives me full
> traceability and no write contention, and the read cost is fixable with an index or a
> materialized snapshot. I'd rather optimize a correct model than debug a fast wrong one.
> That indexing work is the next measured piece I have planned."

*(This is a genuinely strong answer because it names the cost honestly, then justifies it,
then says what you'd do about it. That three-part shape works for any trade-off question.)*

**Other likely probes:** why `process_seq` on a BOM line matters (component due dates come
from the step that consumes it, not the order as a whole); what you deliberately left out
vs. the real system (per-item make-to-stock toggle, effectivity dates, co-products — all in
`docs/FRD.md` §7); why `NUMERIC` in Postgres but `float64` in Go (precision trade-off, fine
for quantities, wrong for money).

### Bullets not yet earned — do not script these yet

D (React/real-time visibility), E (Docker/CI-CD), F (Azure/availability), H (query
optimization 8s→3s) are **pending their milestones** per `docs/RESUME.md`. Don't build
stories for work that doesn't exist yet — a story without evidence is exactly what
collapses under a follow-up.

---

## 5. Handling the attribution question — rehearse this one cold

It *will* come, in some form: "Was this at your job?" / "Was this in production?" / "How
many users?"

**The answer, plainly:**
> "The Salesforce-based product is my day job — that's a real commercial system used by
> manufacturers. This Go engine is my own rebuild of its core planning logic. I did it
> because the platform kept me in Apex and declarative config, and I wanted to go deep on
> backend systems engineering — recursion, query optimization, concurrency."

**Why leading with this is strictly stronger than hedging:**
- Volunteering it reads as integrity. Getting caught hiding it reads as fabrication, and
  ends the interview.
- "I built a substantial system on my own initiative to close a gap in my skills" is a
  *good* signal, not an apology.
- The engineering is real either way. The 16.8× is *your* number, measured on *your* code.

**Never do this:** attach the business-outcome numbers (¥20M revenue, 35% planning
reduction, 99.9% uptime) to this project. Those belong to the real product. If asked how
those were measured, answer about the real job or say you don't have visibility into that
measurement — never invent methodology.

---

## 6. Traps and how to handle them

**"Why didn't you just use an ORM / existing MRP library?"**
Don't get defensive. "For a learning project the point *was* to write the traversal and
tuning myself. In a commercial setting I'd absolutely evaluate existing systems first —
MRP is a solved problem, and rebuilding it is only justified by learning or a genuine
domain mismatch."

**"This is just a CRUD app with one clever query."**
Agree partially, then redirect to the real substance: "The CRUD parts are the boring 20%.
The substance is the explosion algorithm, cycle detection, the transaction boundary that
makes a bad BOM leave zero partial rows, and the measured performance work."

**A question you genuinely can't answer.**
Say so, then show how you'd find out: "I don't know off the top of my head — I'd check the
EXPLAIN plan / I'd measure it before guessing." Interviewers respect this enormously and
punish confident bullshit severely. You have a real advantage here: everything in this
project *was* measured, so "I'd measure it" is your actual, demonstrated habit.

**They find a bug in your code live.**
Best possible outcome, honestly. "Good catch — that's a real bug." Then reason about the
fix out loud. You already have a genuine story here: you caught a **sign error in your own
safety-stock formula** (`FR-4.1` said `− safety_stock`, correct is `+`) by deriving it from
what safety stock is *for* rather than transcribing the spec. That's a great thing to
volunteer — it shows you audit your own specs.

---

## 7. Questions to ask them (have 3 ready)

Asking nothing signals disinterest. Grounded in what you've actually built:
- "How do you handle long-running background work here — job queue, worker pool, something
  managed?" *(You've just designed this; you can go deep on their answer.)*
- "What does your process look like when someone proposes a schema change to a core table?"
- "How much of the performance work is reactive versus measured proactively?"

---

## 8. Practice protocol

1. Record yourself giving the 2-minute B2 opener. Watch it. Most people are 40% too long
   and bury the number.
2. Draw the BOM tree from memory, under 30 seconds, while narrating.
3. Have someone (or Claude, cold) fire the six B2 follow-ups at you out of order, no notes.
4. Rehearse the attribution answer until it's automatic — it's the highest-stakes 20
   seconds in the whole interview, and hesitation there reads as guilt.
5. Re-read `BENCHMARKS.md` before any interview so the numbers are exact, not approximate.
   "About 26 minutes to about a minute and a half, roughly 17×" is right. Vague is fine;
   *wrong* is fatal.
