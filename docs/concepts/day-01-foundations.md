# Day 1 — Foundations: rationale behind the scaffold

Read this before the scratch rebuild (see `CLAUDE.md`, working method step 4). Each
section is a technique with a real alternative that was rejected — the "why," not a
re-description of what the code does (read the code for that). A self-check with no
answers given is at the bottom; use it cold, before looking back at the code, as oral-
defense practice.

## 0. Project structure — the thought process before any file existed

This is the layer above everything else in this doc: before writing `main.go`, the
question was "what are the compiled artifacts, and what do they share?"

**Start from binaries, not packages.** The project needs three things that actually run:
a long-lived server, a one-shot schema-migration tool, and a one-shot data generator.
`cmd/api`, `cmd/migrate`, `cmd/seed` map to that 1:1 — each is a tiny `main.go` that wires
together whatever it needs from `internal/`. This is the standard (if unofficial) Go
convention: `cmd/<binary-name>/main.go` per compiled output.

**Why three binaries instead of one with subcommands** (`mrp-go serve`, `mrp-go migrate`,
`mrp-go seed`, via something like `cobra` or a manual `os.Args[1]` switch)? Real trade-off,
not an obvious win either way: a single binary is one artifact to build and ship, which
matters if this were a CLI tool users install. But `api`, `migrate`, and `seed` are used
completely differently — one runs continuously as a server, the other two are one-shot
tools run manually or from a script/CI step — and each wants its own flag set without
namespacing. For a project this size, three small `main.go` files are easier to read start
to finish than one dispatcher plus subcommand routing. If this grows into something people
install as a CLI, that trade-off flips.

**Why `internal/` specifically, not `pkg/` or a flat root.** `internal/` isn't just a
naming convention — the Go compiler enforces it: nothing outside the module tree rooted at
`internal/`'s parent can import those packages, even across a multi-module workspace. That
constraint is doing real work here: it's a forcing function against accidentally designing
a public API for code that has exactly one consumer (this app). A flat structure (all
`.go` files in the root, or one `pkg/` dumping ground) has no enforced boundary at all and
stops scaling almost immediately once there's more than a handful of files.

**Why `cmd/` sits beside `internal/` and not inside it.** The Go rule precisely: a package
under a directory named `internal` is importable only by code rooted at that `internal`
directory's *parent*. Here, `internal/db`'s parent is the module root — so `cmd/api`,
`cmd/migrate`, `cmd/seed` already have full access as siblings; nesting `cmd/` inside
`internal/` would grant them nothing new. More importantly, everything in `cmd/` is
`package main`, and Go forbids importing `package main` from anywhere, full stop — there is
nothing to protect. `internal/`'s restriction only means something for packages that could
otherwise be imported by something else; entry points are never imported by definition, so
applying "outsiders can't import this" to them is a no-op that costs you the standard
`cmd/`-at-root convention (`go build ./cmd/...`) for zero actual protection gained.

**Why these specific packages under `internal/`, and why *not* others yet:**
- `config` — isolated because "how settings are obtained" (env vars today) is exactly the
  kind of thing that changes independently of everything else (flags, a config file, a
  secrets manager later) and nothing should need to know which.
- `db` — the pgx pool + migration runner live together because both are "how the app talks
  to Postgres as infrastructure," not business logic. Grouping by *what changes together*
  (swap Postgres drivers, change pooling behavior) rather than by *feature*.
- `domain` — plain structs, no behavior, no SQL tags. Kept separate from `repo` on purpose:
  domain types don't know persistence exists; `repo` knows about both domain types and SQL.
  This avoids the common ORM-adjacent trap where your core types quietly become shaped by
  how they're stored rather than what they mean.
- `http` — transport only (routing, decode/encode, status codes). Talks to `repo` for now;
  will talk to a `service` layer once one exists (see §6 in this doc for why there isn't
  one yet).
- `repo` — all SQL, one file per aggregate.
- `mrp` and `jobs` are named as "planned" in `README.md`/`CLAUDE.md` but were **not**
  scaffolded as empty packages on Day 1, on purpose — an empty package with no content is
  a false signal (it implies more progress than exists) and just creates unused-import
  friction until there's a first real thing to put in it. Create a package when you have
  its first real file, not before.

**The layer-first split (handler / repo / domain, cross-cutting all features) vs. the
alternative — feature folders** (`internal/items/` containing its own handler+repo+domain,
`internal/plans/` likewise, etc., sometimes called vertical-slice architecture): this
project picked layer-first, partly because it deliberately mirrors the source Salesforce
system's Controller → Service → Logic → Dao split (see `CLAUDE.md` Architecture section).
Feature folders scale better once there are dozens of features, because each one is
self-contained instead of every layer folder accumulating one file per feature forever.
Layer-first is easier to navigate early and keeps the cross-system mirroring intact. This
one is genuinely close enough to be worth writing your own ADR on rather than accepting
as given — see the self-check below.

## 1. Config loading — `internal/config/config.go`

The whole file is small — a struct and two functions — but the shape is deliberate, not
default:

```go
type Config struct {
    DatabaseURL string
    Port        string
}

func Load() Config {
    return Config{
        DatabaseURL: getenv("DATABASE_URL", "postgres://mrp:mrp@localhost:5433/mrp"),
        Port:        getenv("PORT", "8090"),
    }
}

func getenv(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

**Why a `Config` struct returned by `Load()`, not package-level vars set in `init()`.**
Compare the two shapes directly:

```go
// Pattern A — package-level vars + init(); used from anywhere as config.DatabaseURL
var DatabaseURL string
func init() { DatabaseURL = getenv("DATABASE_URL", "postgres://...") }

// Pattern B — Load() returning a value; used as cfg := config.Load()
type Config struct{ DatabaseURL string }
func Load() Config { return Config{DatabaseURL: getenv("DATABASE_URL", "postgres://...")} }
```

Three concrete situations where the difference actually bites:

1. **Debugging: "where did this value come from?"** `init()` runs automatically the
   instant the package is imported — before `main()` even starts, by *anything* that
   imports `config`, even transitively. If a value looks wrong, there's no call site to
   jump to. `Load()` has exactly one: grep for `config.Load(`.
2. **Testing: shared mutable state.** With Pattern A, `config.DatabaseURL` is one variable
   shared by the whole test binary, already set before any test runs — a test can't even
   re-trigger `init()` by changing the env var later, only hand-mutate the global directly,
   and parallel tests (`t.Parallel()`) race on that same variable. With `Load()`, each test
   just builds its own `Config{...}` literal — no shared state, nothing to reset.
3. **Surprise coupling through imports.** If `internal/mrp` imports `internal/domain`, and
   `domain` happens to import `config` for anything, then a pure-math unit test for BOM
   logic — nothing to do with the database — transitively triggers `config.init()` and
   reads environment variables it never asked for. `Load()` can't do this: nothing happens
   until something explicitly calls it.

One-line version: `init()`-based config is a light that switches on the moment someone
opens the door, whether they wanted it or not, with no way to ask who flipped it. `Load()`
is a light switch — nothing happens until a hand is on it, and you can always find the hand.

**Why not just call `os.Getenv` directly wherever a setting is needed** (in `db.Connect`,
in `main.go`, wherever) instead of centralizing it here? Two reasons: first, this file
becomes the single place that documents "what can I configure on this app" — answerable by
reading twenty lines instead of grepping the whole tree for `os.Getenv`. Second, it means
`db.Connect` and the HTTP server take a plain string parameter, not "go read the
environment yourself" — which is what actually makes them independently testable and
reusable (e.g. `cmd/seed` reuses the exact same `config.Load()` and `db.Connect`, no
environment-reaching duplicated between binaries).

**Why the tiny `getenv` helper instead of inlining the check twice in `Load()`.** It's the
smallest reasonable abstraction for a two-line pattern repeated per field — not reaching
for a config library (viper, envconfig, struct tags) for two settings. That trade-off
flips once there are a dozen+ settings, nested sections, or a need to fail loudly on a
missing required value instead of silently falling back — worth naming as a real
threshold, not "this is always enough."

**The fallback values themselves (`5433`, `8090`) are this-machine facts, not a design
choice** — port `5432`/`8080` were already taken locally by other projects. Worth
remembering that distinction: some things in code are principled decisions, others are
just "what makes it run here today," and conflating the two when explaining your own code
is a tell.

## 2. Domain structs — `internal/domain/models.go`

Small file, but every field shape here is a translated decision, not a copy-paste of the
SQL schema:

```go
type Item struct {
    ID           int64    `json:"id"`
    Code         string   `json:"code"`
    Name         string   `json:"name"`
    ItemType     string   `json:"item_type"`
    UOM          string   `json:"uom"`
    LeadTimeDays int      `json:"lead_time_days"`
    LotSizeRule  string   `json:"lot_size_rule"`
    FixedLotSize *float64 `json:"fixed_lot_size,omitempty"`
    SafetyStock  float64  `json:"safety_stock"`
}

type ProductionPlan struct {
    ID          int64     `json:"id"`
    Code        string    `json:"code"`
    ItemID      int64     `json:"item_id"`
    ItemCode    string    `json:"item_code"`
    ItemName    string    `json:"item_name"`
    Qty         float64   `json:"qty"`
    DueDate     time.Time `json:"due_date"`
    WarehouseID int64     `json:"warehouse_id"`
    Status      string    `json:"status"`
    CreatedAt   time.Time `json:"created_at"`
}
```

**Type mapping choices, and where they're honest simplifications.** Postgres
`BIGINT GENERATED ALWAYS AS IDENTITY` → Go `int64`, deliberately, not the platform-width
`int` — this documents an exact match to the DB type rather than relying on `int` happening
to be 64 bits on your machine. `NUMERIC` → `float64` is the more interesting one: it's
simple and fine for physical quantities like `qty`, but **it's a real precision trade-off**
you should be able to name unprompted — float64 can't represent every decimal exactly, and
for money or anything requiring exact decimal arithmetic you'd want a fixed-point/decimal
type instead. Using `float64` here is a legitimate choice for this domain, not something to
present as universally correct.

**Why `FixedLotSize` is `*float64`, a pointer, not a plain `float64`.** The column is
nullable — `fixed_lot_size` is only set when `lot_size_rule = 'fixed'`. A plain `float64`
can't represent "absent"; zero would be indistinguishable from "no fixed lot size," which is
a different fact entirely (0 means something; NULL means the concept doesn't apply). A
pointer can be `nil`, which maps naturally to SQL `NULL`, and `omitempty` means the JSON
response drops the field entirely rather than emitting `null` or a misleading `0`. The
alternative, `sql.NullFloat64`, is the database/sql-idiomatic type for this — rejected here
specifically because it's a `{Float64, Valid bool}` pair that's awkward in application code
and doesn't serialize to JSON the way you'd want without extra work; a plain pointer is
simpler for a type that has to cross both the DB boundary *and* the JSON boundary.

**Why `ProductionPlan` carries `ItemCode`/`ItemName` even though the `production_plans`
table only stores `item_id`.** This is the tell that a domain struct's shape is driven by
*what the application needs to hand back*, not by mechanically mirroring one table.
`ItemCode`/`ItemName` come from the join in `repo.ListPlans` (`production_plans` joined to
`items`) — the struct matches the query result it's actually populated from, because an API
client asking "list my plans" needs to display the item's name, not force a second lookup
by ID. One domain struct does not have to equal one table.

**The asymmetry worth being honest about:** this file has zero SQL awareness (no `db`
tags, no `sql.Null*` types — reinforcing the point from §0 that `domain` doesn't know
persistence exists) but it *does* have `json` tags, meaning it's doing double duty as both
the core vocabulary type and the HTTP response shape. The "purer" version would keep
`domain` fully presentation-agnostic too, with a separate DTO type in `internal/http` that
maps domain → JSON shape. This project takes the pragmatic shortcut for a small CRUD
surface — reasonable now, but the shortcut has a cost: if the JSON API ever needs to diverge
from the domain model (versioning, hiding an internal field), a real DTO layer becomes
necessary at that point, not before.

## 3. Graceful shutdown — `cmd/api/main.go`

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
go func() { srv.ListenAndServe() /* ... */ }()
<-ctx.Done()
srv.Shutdown(shutdownCtx)
```

`signal.NotifyContext` turns OS signals into context cancellation — one idiom instead of
a raw `signal.Notify` + channel + `select`. The server runs in a goroutine specifically so
`main` can block on `<-ctx.Done()`; calling `ListenAndServe()` directly on the main
goroutine would leave no way to react to a signal while it's blocking forever.

`ListenAndServe` **always** returns a non-nil error on shutdown — that's why the check is
`!errors.Is(err, http.ErrServerClosed)`. A genuine bind failure should crash and unblock
`main` early via `stop()`; a normal shutdown-triggered error should not be treated as
fatal. This is what FR-11.2 in the FRD is about: draining in-flight requests instead of
killing them mid-write matters once there's an in-progress MRP transaction you don't want
torn in half.

## 4. Retry-connect — `internal/db/db.go`

**What `*pgxpool.Pool` actually is, first:** a managed set of already-established, reusable
Postgres connections, not a new TCP+auth handshake per query — that handshake is expensive,
and Postgres caps concurrent connections (~100 by default), so a pool amortizes the cost and
bounds how many connections the app opens. It's safe to share across goroutines, which is
why one `*pgxpool.Pool` gets created in `Connect` and handed to every handler, `cmd/migrate`,
and `cmd/seed` alike — each call to `pool.Query`/`pool.Exec` borrows a connection for just
that call's duration. `pgx` ships its own pool rather than using `database/sql`'s generic
one because it needs to expose Postgres-specific behavior standard `database/sql` can't —
notably the `COPY` protocol (`pool.CopyFrom`) the seeder leans on.

**Pooled connections are persistent, not re-handshaked per query** — same idea as keeping
a WebSocket open, reused for many messages instead of reconnecting each time. The reason
it's a pool of *several* persistent connections rather than just one: Postgres's wire
protocol is one-query-at-a-time per connection, it can't multiplex concurrent queries over
a single connection the way WebSockets/HTTP2 multiplex frames. A pool of size 1 would work,
but every goroutine needing the DB would queue behind whichever query is currently running,
serializing all database access down to one at a time — even though Postgres itself runs
each connection as its own backend process on the server and can genuinely execute several
queries in parallel. Pool size is really "how many DB operations do I want truly
in flight at once": too small (like 1) bottlenecks and serializes; too large risks
exceeding Postgres's own connection cap for connections that mostly sit idle.

**What `Connect` does, end to end:** (1) `pgxpool.New(ctx, url)` builds the pool object —
this does *not* connect yet, just parses the URL and sets config; (2) the retry loop below
confirms the DB is actually reachable before handing the pool back; (3) success → return
the ready pool; (4) exhausting all attempts → close the pool (don't leak it) and return a
wrapped error. Net effect: every caller (`cmd/api`, `cmd/migrate`, `cmd/seed`) gets back
either a pool that's *known-good*, or an error — never a pool that might silently fail on
first real use.

**Why `Ping` is needed at all:** `pgxpool.New()` doesn't connect to anything — it only
parses the connection string and sets up the pool's config; actual connections are opened
lazily, on first use. Without a ping, `Connect` could report success with a wrong host, bad
credentials, or Postgres simply not running, and the failure would only surface later, on
whatever request happens to hit the DB first — a far worse place to discover a startup
problem. `Ping` forces one real round trip immediately, so the app fails fast, at startup,
with a clear error, rather than accepting traffic and failing mysteriously later. Same
well-known gotcha as `database/sql`'s `sql.Open`, which is equally lazy.

`docker compose up -d` returns before Postgres finishes initializing. A single `Ping`
right after can legitimately fail even though the DB will be ready in a second or two.
The retry loop (10 attempts, ~1.3s apart) buys ~13s of grace — cheap insurance for local
dev, not a production retry strategy (no backoff, no jitter, no circuit breaker; a real
production system would more likely lean on orchestration-level readiness — Docker
Compose `depends_on: condition: service_healthy`, Kubernetes readiness probes — instead of
or alongside retrying in application code). The `select` on `ctx.Done()` inside the loop
matters: without it, Ctrl+C during a slow DB startup would hang instead of exiting.

**Why a separate `pingCtx` instead of passing the outer `ctx` straight to `Ping`.** The
outer `ctx` may have no deadline at all — it's ultimately `context.Background()` wrapped
only with signal-cancellation from `main`. If `Ping` used it directly, a single attempt
could hang indefinitely if Postgres is unreachable in a way that doesn't error quickly
(dropped connection, no response) — blocking the *entire* retry loop on attempt #1.
`pingCtx := context.WithTimeout(ctx, 3*time.Second)` bounds *this one attempt* to 3s while
still inheriting the outer `ctx`'s cancellation, since it's derived from `ctx` rather than
a fresh `context.Background()` — if the outer context cancels, `pingCtx` cancels too. The
`cancel()` right after each attempt just releases `pingCtx`'s timer resource immediately
instead of leaving it dangling.

**The `select` block after a failed ping:**

```go
select {
case <-ctx.Done():
    pool.Close()
    return nil, ctx.Err()
case <-time.After(time.Second):
}
```

Two channels racing, whichever fires first wins. `<-ctx.Done()` fires if the *outer*
context gets cancelled (Ctrl+C) while this attempt was failing — if so, stop immediately:
close the pool (don't leak it) and return the cancellation error, no point waiting out the
rest of the loop. `<-time.After(time.Second)` is a fresh 1-second timer created each time
this `select` runs — if nothing cancels within that second, it fires and the `for` loop
moves to the next attempt. Net effect: wait 1s between retries, *unless* the caller cancels
first, in which case bail out right away instead of finishing that wait — this is what
keeps Ctrl+C responsive even mid-retry, instead of hanging for up to a second after the
program was already told to stop.

## 5. Embedded migrations, hand-rolled — `internal/db/migrate.go`

**The problem, in plain terms.** A "migration" is just a saved script of SQL that makes
one specific change to the database (add a table, add a column), saved as a numbered file
so the order is clear — `0001_init.sql` is migration #1. If you just ran that file's SQL
every time the app started, the second run would try to create tables that already exist
and crash. This function's whole job is to remember what's already been done and only run
what's new.

**What `//go:embed` does, concretely.** Normally a compiled Go program is one file, and any
other files it needs (like a `.sql` file) stay separate on disk — the program has to be
told where to find them. `//go:embed migrations/*.sql` tells the compiler to bake the
actual *text* of every matching file directly into the compiled program. The result: one
executable that already contains the SQL inside it, nothing separate to forget to copy
when deploying. `migrationsFS` (an `embed.FS`) behaves like a tiny folder of files that
lives inside the program, not next to it.

**"Virtual filesystem," precisely what that means.** `migrationsFS` behaves like a
filesystem — folders, paths, `ReadFile`, `Glob`, the same operations — but isn't backed by
real files on disk while the program runs. Contrast with `os.ReadFile("migrations/0001_init.sql")`,
a *real* filesystem read: that depends on an actual file existing at that exact path at
runtime, and breaks if the binary moves without that folder alongside it. With `embed.FS`,
you could delete the entire `internal/db/migrations/` folder *after* `go build` and the
already-compiled program keeps working — it's not reading from that folder anymore, because
the compiler already copied the bytes into the binary. "Virtual" = a filesystem-*shaped* API
over data that's really just bytes sitting in the running program's memory, not real files.

**`//go:embed migrations/*.sql` / `var migrationsFS embed.FS`, precisely.** That comment
is not a regular comment — Go's build tool specifically recognizes comments starting with
`//go:embed` immediately above a variable declaration. At `go build` time, it finds every
file matching the pattern, reads their bytes, and bakes them into the compiled binary,
wiring `migrationsFS` to behave like a small filesystem containing exactly those files —
nothing is assigned to it in the source; it's empty in the code and the compiler fills it
in. Concrete picture: photocopying a stack of papers and stapling the copies directly into
a book instead of keeping them in a folder beside it — wherever the book goes, the papers
go with it. Right now `migrations/*.sql` matches exactly one file — `0001_init.sql`, the
only one that exists — but it's a wildcard, not a hardcoded filename: add `0002_...sql`
into that folder and rebuild, and both get embedded automatically, no change needed to the
`//go:embed` line itself. Once populated, `fs.Glob(migrationsFS, "migrations/*.sql")` lists
matching paths and `migrationsFS.ReadFile(path)` reads a file's bytes, both from memory inside the
running program, never touching disk.

**`fs.Glob(migrationsFS, "migrations/*.sql")`, from scratch.** "Glob" isn't Go-specific —
it's the same idea as typing `ls *.txt` in a terminal: a filename pattern with a wildcard
(`*` = "anything") that matches multiple files at once. `"migrations/*.sql"` means "inside
a folder called `migrations`, match any file ending in `.sql`." Crucially, `Glob` only
returns **names**, it never opens or reads a file's content — like asking a librarian "what
titles start with 'The'?" and getting a list of titles back, not the books themselves;
reading content is the separate, later `migrationsFS.ReadFile(path)` step, once per path.
Right now `migrationsFS` holds one file, so the call returns `entries = ["migrations/0001_init.sql"]`
and `err = nil` (zero matches would *also* give `err = nil` — an empty result isn't an
error; `err` is only set if the search itself breaks, e.g. a malformed pattern). Add a
second matching file and `entries` would have two items — `Glob` doesn't know or care how
many matches exist ahead of time. It can search `migrationsFS` specifically, not just real
folders, because Go has a general rule (an "interface") that anything able to answer "what
files do you have" counts as a filesystem regardless of where its data lives — a real disk
folder qualifies, so does `embed.FS` — so `fs.Glob` was written once, against that shared
rule, and works identically on both.

**How multiple embedded files are told apart:** by path/filename string, the only
identifier there is — same as a real filesystem, two files can't share a name in the same
folder. With both `0001_init.sql` and a future `0002_add_resource_orders.sql` present,
`migrationsFS` internally looks like a tiny in-memory directory:

```
migrations/
  0001_init.sql                 → [bytes of that file's content]
  0002_add_resource_orders.sql  → [bytes of that file's content]
```

`fs.Glob` would return `["migrations/0001_init.sql", "migrations/0002_add_resource_orders.sql"]`
— note Glob's return order isn't guaranteed already sorted, which is exactly why the code
calls `sort.Strings(entries)` right after rather than trusting it; `0002` might assume
something `0001` already created. Then `version := path[len("migrations/"):]` **strips the
folder prefix**, leaving the bare filename (`"0001_init.sql"`) — that bare name, not the
full path, is what's checked against the `applied` map and written into
`schema_migrations.version`. The **full path** (`"migrations/0001_init.sql"`) is a
different, separate key: it's what `migrationsFS.ReadFile(path)` needs to fetch that one
file's actual SQL bytes. Two levels of the same identifier, used for two different jobs —
full path to *read content*, bare filename to *track what's applied*.

`//go:embed` only works on a *package-level* variable — a hard Go rule, won't compile
inside a function — which is why this lives at the top of the file, not inside `Migrate`.
Worth noticing this doesn't repeat the mistake rejected for `config` (§1): that was about
*mutable* state read from the *environment* at *import time*, with unpredictable ordering.
This is the opposite — fixed, read-only data baked in at *compile* time, identical on every
run, no I/O, no environment dependency. Functionally closer to declaring a big constant
than to hidden mutable global state.

**Walking through what it does, step by step:**
1. **Make sure a "checklist" table exists** — `CREATE TABLE IF NOT EXISTS schema_migrations`.
   One row per migration already applied. `IF NOT EXISTS` means running this a hundred
   times in a row is always safe — that "safe to repeat, same result every time" property
   is what "idempotent" means; the word is just a label for that idea.
2. **Read the checklist** — query which migrations are already marked done, put those
   names into a Go map (a fast lookup list — like a guest list you can check a name
   against).
3. **Find every migration file that exists** in the embedded files, sort the filenames —
   since they're numbered `0001_`, `0002_`, alphabetical sort is also chronological order,
   which matters because migration #2 might assume something migration #1 created.
4. **For each file, in order: skip it if it's on the checklist, otherwise run it.**
   "Running it" bundles several things into one **transaction** — telling the database
   "this whole bundle succeeds together, or none of it does; if anything fails partway,
   put everything back like it never happened":
   - read the SQL text out of the embedded file
   - run that SQL
   - *in the same bundle*, add a row to the checklist marking this migration done
   - if everything worked, **commit** (make it permanent); if anything failed, **rollback**
     (undo the whole bundle)

   **`tx, err := pool.Begin(ctx)`, precisely.** This borrows *one specific connection* from
   the pool and sends it a `BEGIN` command, returning `tx` — a handle representing "this one
   connection, currently inside a transaction." From here until `tx.Commit(ctx)` or
   `tx.Rollback(ctx)`, every statement in this bundle must go through `tx`, not `pool`
   directly — not a style choice: a transaction is a property of one specific connection,
   which Postgres tracks per-connection, not globally. Calling `pool.Exec()` twice hoping to
   bundle two statements could hand back two *different* connections (the pool is shared
   across every caller), and Postgres would see two unrelated operations with no bundling at
   all. `tx` pins one connection for the whole bundle, guaranteeing every statement run
   through it is genuinely part of the same all-or-nothing group.

   The one subtle but important detail: why bundle "run the SQL" and "mark it done" into
   the *same* transaction instead of two separate steps? If they were separate and the
   program crashed exactly between them, the SQL would have run but the checklist wouldn't
   know it — next startup would try to reapply a migration that already happened, and
   crash on tables that already exist. Bundling means either both happen or neither does;
   no broken in-between state is possible.
5. **Log it** — print a message once a migration succeeds, so whoever's watching startup
   can see progress.

**One-sentence summary:** this function finds what hasn't been applied yet and applies
each new thing exactly once, safely, in order — so it's safe to call on every single app
startup, and it will only ever do new work, never repeat or half-apply old work.

Not golang-migrate or goose — honestly, for simplicity and one less dependency, **not**
because hand-rolling is best practice.

**The honest gap:** no down-migrations. That's a deliberate scope cut appropriate for a
personal project with one contributor — not something to defend as correct at a company
with a team touching the schema concurrently. Say that plainly if asked.

**What `schema_migrations` concretely is.** Not a metaphor — it's a real table living in
the same Postgres database as the actual data, with exactly two columns: `version` (the
migration's filename, e.g. `0001_init.sql`) and `applied_at`. It *is* the checklist from
step 1/2 above, literally the table queried and written to.

**Where it's actually defined — not in any `.sql` file.** It's created by a raw SQL string
written directly inside `migrate.go` itself (`pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS
schema_migrations (...)")`, step 1), not one of the numbered migration files. Reason: a
chicken-and-egg problem. The table's whole job is answering "has this migration already
run?" — but checking that requires querying the table, which doesn't exist yet on a brand
new database, so it can't track its own creation the way it tracks everything else.
Sidestep: don't try to track it — just unconditionally run `CREATE TABLE IF NOT EXISTS`
every single time `Migrate()` is called. Since that statement is safe to repeat forever
(a no-op once the table exists), there's nothing to track; it simply ensures the table is
there before anything else happens.

**Adding a table later means a new numbered file, never editing an already-applied one.**
Once a migration has actually run somewhere — and `0001_init.sql` already has, against the
local dev Postgres — the runner only tracks it *by filename*, not content. Edit
`0001_init.sql` after the fact and your own machine won't notice (it already has that
filename marked done, so it never re-reads it), but a teammate or a fresh deploy running
migrations for the first time *would* get the edited version — two environments now
disagree about what "migration 0001" means, silently. The fix is always a new file,
`0002_<description>.sql`, e.g.:

```sql
-- 0002_add_resource_orders.sql
CREATE TABLE resource_orders (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    work_order_id BIGINT NOT NULL REFERENCES work_orders(id),
    resource_id   BIGINT NOT NULL REFERENCES resources(id),
    start_time    TIMESTAMPTZ NOT NULL,
    end_time      TIMESTAMPTZ NOT NULL
);
```

Drop it in `internal/db/migrations/`, run `cmd/migrate` again — it lists every `.sql` file,
skips `0001` (already on the checklist), applies only `0002`. (Editing `0001` earlier for
the `ProductionPlan.Code`/`Name` question was fine specifically *because* nothing had
shipped anywhere else yet — that exception doesn't apply once something's out in the
world.)

## 6. Layering — `router.go` → `handlers/` → `repo/`

Handlers only decode/validate/respond — no SQL. Repo files hold all SQL, one per
aggregate. No service layer yet, because Day 1's logic was pure CRUD — a service layer
earns its place once there's real business logic to isolate from both HTTP and SQL
concerns (that's exactly where FR-3's MRP explosion will live).

Worth noticing in `repo/plans.go`'s `CreatePlan`: it's a two-step insert — insert the row
to get the generated `id`, then a second `UPDATE` to set `code = 'PP-' || id` — because a
row can't reference its own just-generated identity value within the same `INSERT`'s
values list.

## 7. The seeder — `cmd/seed/main.go` (the densest file)

**Bulk insert via `pgx.CopyFrom`, not `INSERT` loops.** Postgres's `COPY` protocol skips
per-row parse/plan/bind overhead — this is why 200k ledger rows took ~13 seconds total
instead of minutes. This is *not* the "naive version" that gets optimized later — realistic
seeding needs to be fast on its own; that's a separate concern from the MRP-engine
naive-vs-optimized story.

**Reading IDs back after `CopyFrom`, by code prefix.** `COPY` can't `RETURNING` generated
IDs the way `INSERT` can. So after inserting all items, the code does
`SELECT id, code FROM items ORDER BY id` and buckets by prefix (`RAW-`, `SUB-`, `FG-`) into
the `catalog` struct. This only works because seeding is single-writer, immediately after a
fresh `TRUNCATE ... RESTART IDENTITY` — insert order reliably equals ID order. **Name this
fragility out loud** — it would break under concurrent writers.

**Building the BOM tree by construction, not random-edges-then-cycle-check.** `seedBOMs`
only ever lets a subassembly at level N reference items at level N−1 or raw materials —
never sideways or up. That makes the tree acyclic *by construction*: no validation pass,
no retry-on-cycle-detected loop. General technique: when you can make an invalid state
structurally unrepresentable, prefer that over generate-then-validate.

**`process_seq` is tied to each item's real step count**, not an arbitrary number —
`addLine` computes `seq := (1 + rng.IntN(stepCounts[parent])) * 10`, so every BOM line
lands on a routing step that actually exists. This matters for FR-3.3 later: component due
dates come from the step that consumes them, so seed data referencing a non-existent step
would silently break that requirement without anyone noticing until Day 2.

**Deterministic RNG (`-seed` flag, default 42).** `math/rand/v2`'s `PCG` source, seeded
explicitly rather than time-based, means re-running the seeder reproduces the *same*
dataset. That's what makes `BENCHMARKS.md` numbers comparable before/after an
optimization — if the dataset changed between runs, an improvement could just be "got
lucky with less data," not a real fix.

**Batching the ledger insert in 50k chunks**, not one `CopyFrom` for all 2M rows. Building
a 2M-row `[][]any` in memory before shipping it is wasteful, and a single giant `COPY`
gives zero progress feedback. Batching trades a little overhead for visibility
(`slog.Info("seeding movements", "written", ...)`) and bounded memory.

## Self-check — answer cold, no peeking, then compare against the sections above

0a. Why `cmd/api`, `cmd/migrate`, `cmd/seed` as three separate binaries instead of one
    binary with subcommands? What would make that trade-off flip?
0b. What does `internal/` actually enforce (not just suggest), and why does that matter for
    a project that's currently a single application, not a library?
0b-i. Why doesn't nesting `cmd/` inside `internal/` change what `cmd/api` can import? What
    Go rule makes a `main` package fundamentally different from every other package here?
0c. Why do `mrp` and `jobs` not exist as empty packages yet, even though they're on the
    roadmap?
0d. Layer-first (`handlers/`, `repo/`, `domain/` each spanning all features) vs. feature
    folders (`internal/items/`, `internal/plans/`, each self-contained) — what does each
    scale better toward, and which did this project pick, and why? Do you agree, or would
    you write the ADR differently?
1. Why does `config.Load()` return a `Config` value instead of the package exposing global
   vars set in `init()`? What does that buy you for testing specifically?
2. Why centralize env-var reads in `config` instead of calling `os.Getenv` directly inside
   `db.Connect` or `main.go` where each value is actually used?
3. Why is `Item.FixedLotSize` a `*float64` instead of a plain `float64`, and why not
   `sql.NullFloat64` instead of a pointer?
4. Why does `ProductionPlan` include `ItemCode`/`ItemName` when the `production_plans`
   table only stores `item_id`? What does that reveal about what actually shapes a domain
   struct's fields?
5. `domain` has no `db` tags but does have `json` tags — what's the asymmetry there, and
   what would it cost to make `domain` fully presentation-agnostic too?
6. Why does `ListenAndServe` returning a non-nil error on shutdown *not* mean something
   went wrong? What's the one case where a non-nil error here should still be fatal?
7. What problem does the retry loop in `db.Connect` solve, and why is 10 attempts × ~1.3s
   a reasonable choice for local dev specifically (not production)?
8. Why hand-roll the migration runner instead of using golang-migrate? What did that
   choice give up?
9. Why does `repo/plans.go`'s `CreatePlan` need two statements (`INSERT` then `UPDATE`)
   instead of setting `code` in the original `INSERT`?
10. Why doesn't `pgx.CopyFrom` let you get generated IDs back directly, what did the seeder
    do instead, and under what condition would that workaround silently produce wrong
    results?
11. Why build the BOM tree level-by-level instead of generating random parent/child edges
    and checking for cycles afterward?
12. Why does `process_seq` in seeded BOM lines get computed from `stepCounts[parent]`
    instead of a fixed range like 10-50?
13. Why does the seeder take an explicit `-seed` flag instead of seeding the RNG from the
    current time?
14. Why does `seedMovements` write in 50k-row batches instead of one `CopyFrom` call for
    all 2,000,000 rows?
