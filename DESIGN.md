# gesso — design

Status: **v0.8.0 released; v0.9 on main — the v1 surface sealed (§14)** (2026-10-29). §12 lists the phases.

This is the document the implementation is written against. Where the code and this
document disagree, one of them is a bug; decide which, and fix both.

## 1. The problem

Collage renders pages and never does backend work: no database, no domain model, no
JSON API. That is deliberate and stays so. An application built on collage still
needs a backend, and in Go the usual answer is a router plus a pile of hand-rolled
conventions that every project re-invents, each time a little incomplete.

The author's previous backend (a chi + PostgreSQL API, ~62k lines, 29 modules) is the
reference for what that pile looks like. Its layout was right; its plumbing was not:

- **No `context.Context`.** 228 queries ran without one. A client that hung up or a
  request that timed out kept its query running; tracing had nothing to follow.
- **Singletons.** `db.Get()`, `config.Get()`, `cache.Get()` called inside
  constructors. Dependencies were invisible, and tests had to set and reset global
  state.
- **Transactions trapped in repositories.** A service could not run two
  repositories in one transaction.
- **Hand-written scanning.** A 45-column `Scan` in positional order, a 40-field
  response copy, an N+1 lookup whose errors were swallowed.
- **Errors bound to one driver.** The HTTP error mapper imported `lib/pq` and found
  the conflicting field by splitting constraint names with a regex.
- **Drift.** Pagination limits written per handler and disagreeing with each other;
  OpenAPI written as comments, separately from the code it described.
- **No lifecycle.** Background jobs started with `go` and were never stopped; the
  database closed under them on shutdown.

gesso is that layout with that plumbing done once, correctly.

## 2. Goals

1. **A module is a folder.** `module.go`, `handler.go`, `service.go`,
   `repository.go`, `queries.go`, `models.go`, `migrations/`, all under `internal/`.
   The layout is gesso's opinion; the code inside it is the application's.
2. **Dependencies are passed, never fetched.** Constructor injection, wired by hand
   in one place. No singletons, no `Get()`, no runtime container, no reflection.
3. **`ctx` everywhere.** A handler receives the request's context; every service and
   repository method takes `ctx context.Context` first. It is a rule, checked by
   `gesso check`.
4. **Typed handlers.** `func(ctx, In) (Out, error)`. Input is decoded from path,
   query and body by struct tags, then validated; the handler never sees invalid
   input. OpenAPI is generated from the same types, so it cannot drift.
5. **Every error is problem+json.** RFC 9457, with a stable machine-readable `code`,
   including the router's own 404 and 405.
6. **Mockable by construction.** Small interfaces, defined by their consumer;
   in-memory fakes for every contract gesso defines; a test suite (`gessotest`) for
   both unit and integration tests.
7. **No ORM.** The application writes SQL. gesso runs migrations and generates typed
   query code from SQL (sqlc-style), not SQL from models.
8. **Works with collage, needs nothing from it.** gesso is an `http.Handler`: mount
   it in collage with `app.Handle`, or run it as its own process. Neither imports the
   other.
9. **Few dependencies.** See §4.

## 3. Non-goals

- An ORM, a query builder, or code that writes SQL from structs.
- A runtime DI container, decorators, or reflection-driven wiring.
- Rendering HTML. That is collage's job.
- Choosing the database. gesso ships dialects as SQL text; the application imports
  the driver.
- `any` in the exported API (§10).

## 4. Dependency policy

- The standard library and `golang.org/x/*` are free to use.
- Anything else is a joint decision, recorded in §13, with the reason.
- Implementations that need a third-party SDK (S3, Redis, a mail provider) live in
  their own repositories (`gesso-s3`, `gesso-redis`, …), never in gesso.
- gesso imports no database driver.

The router is `net/http.ServeMux` (method and `{param}` patterns, Go 1.22+) with a
thin layer for groups and middleware. No chi.

## 5. Layout

```
internal/
  events/
    module.go        // New(Deps) *Module; Routes; optional Start/Stop
    handler.go       // typed handlers
    service.go       // business rules; knows nothing of HTTP
    repository.go    // consumer-side interface + implementation
    queries.sql      // input to code generation
    queries.gen.go   // generated, never edited
    models.go        // inputs, outputs, domain types
    migrations/
      0001_create_events.up.sql
      0001_create_events.down.sql
cmd/api/main.go
```

## 6. Dependency injection

```go
type Deps struct {
    DB      db.DB
    Storage gesso.Storage
    Clock   gesso.Clock
}

func New(d Deps) *Module {
    repo := newRepository(d.DB)
    return &Module{svc: NewService(repo, d.Storage, d.Clock)}
}

func (m *Module) Routes(r *gesso.Router) {
    r.Get("/events/{id}", gesso.Handle(m.get))
}
```

`main` builds every dependency once and passes it down. Modules that depend on one
another take a small interface the consumer declares, never another module's
concrete service. Side effects that cross modules (invalidate a cache when an event
changes) go through an in-process event bus, not through a direct reference.

Generating the wiring (in the manner of `wire`, at build time) is possible later; a
runtime container never is.

## 7. Request pipeline

```
middleware → authenticate → guard → decode + validate → interceptor → handler → error mapper
```

- **Middleware**: plain `func(http.Handler) http.Handler`. `Config.Middleware`
  wraps every module, outside what a module adds with `Use`.
- **Authenticate**: an `Authenticator` finds the identity (`gesso.IdentityFrom(ctx)`).
  `Config.Authenticator` is every module's to start with — a token issuer is the
  app's, not each module's dependency — and a router replaces it with
  `Authenticate` (a partner API with signed requests).
  No credential is anonymous; a credential that is there but malformed, expired or
  revoked is a 401, never anonymous.
- **Guard**: decides whether the request may proceed (identity, role), before the
  input is read, so a refused caller learns nothing of what the input should be.
  Router guards cover plain handlers too; `gesso.Guards` adds a route's own.
- **Decode + validate**: path, query and body into the handler's input type.
  Query fields are declarative: `query:"take" default:"20" validate:"max=50"`.
  A body cannot set a path, query or header field; such a key is rejected.
- **Interceptor**: wraps a typed handler's call (timing, deadlines, logging,
  translating its error). Not transactions: the DB is passed explicitly and
  `db.InTx` is the service's to call.
- **Error mapper**: typed errors to problem+json. Field names in validation errors
  are the JSON names, not the Go names; messages are i18n keys.

**Surfaces** are first-class: a group of routes under a prefix with its own
middleware stack (an app API, an admin API, a partner API with signed requests).

## 7b. Around the pipeline

What runs before routing, in `App.ServeHTTP`, because no route would see it:
the request id (made, or a trusted proxy's), the access log, the health probes,
and CORS — a preflight is an OPTIONS request no route has, so route middleware
never meets it. A preflight is answered only for a path and method a route
has; anything else falls through to the router's own 404 or 405, so CORS cannot
make a route appear. `"*"` with credentials is refused at New.

Uploads are input like the rest: `form` tags, a `*File` whose ContentType is
sniffed (`http.DetectContentType`) rather than believed, a Filename with its
folders cut off, `accept` and `maxsize` checked while decoding, the route's
`MaxBody` bounding the whole body before parsing, and the multipart temporary
files removed when the handler returns. An input reads a form or a JSON body,
never both — New refuses the type.

Health is the app's, not a module's: readiness must know the app is stopping.
Its answer names the failed checks, never their errors (a probe needs no more,
and an attacker would like the database's address).

## 8. Contracts and implementations

gesso defines an interface only when (a) gesso itself consumes it, or (b) several real
implementations are usual. Everything else the application defines, consumer-side.

| Contract | Package | Reference implementation | Fake | Suite |
|---|---|---|---|---|
| `Storage` | `storage` | `Disk` (through `os.Root`), `s3.Bucket` (SigV4, stdlib) | `Memory` | `storagetest.Run` |
| `Cache` | `cache` | `Memory` (LRU, TTL), `redis.Cache` (RESP2, stdlib) | `Memory` | `cachetest.Run` |
| `Mailer` | `mail` | `SMTP` (STARTTLS), `Log` (envelope only) | `Capture` | — |
| `Clock` | `clock` | `System` | `Manual` | — |
| `Hasher` | `password` | `Argon2id` (`x/crypto`, PHC) | `Argon2id{Cheap}` | — |
| `token.Store` | `token` | SQL | memory | `tokentest.Run` |
| `Locker` | `lock` | `Postgres` (advisory locks), `MySQL` (`GET_LOCK`) | `Memory` | `locktest.Run` |
| `Queue` | `queue` | `SQL`: PostgreSQL (`SKIP LOCKED`), MySQL, SQLite (outbox) | `Memory` | `queuetest.Run` |
| `ratelimit.Store` | `ratelimit` | `SQL` (one table every replica shares) | `Memory` (per process, bounded) | `ratelimittest.Run` |
| `idempotency.Store` | `idempotency` | `SQL` | `Memory` | `idempotencytest.Run` |

Every suite runs against both the reference implementation and the fake, so the
fake cannot drift from what it stands in for. Any implementation — gesso-s3, the
application's own — passes the same suite.

Contracts are bytes and plain types, never `any`: `cache.GetOrLoad[T]` puts the
types back as JSON. Storage holds bytes under keys; what a file is, the
application records in its database.

A Postgres lock lives on the connection that took it, held out of the pool while
the lock is; a connection that could not release its lock is ended, never
returned to the pool, where it would hold the lock for ever. Tested: contention,
waits, cancelled waits, a holder whose session dies, and — with a database/sql
driver that grants every lock and refuses every unlock — a live connection whose
unlock fails or reports nothing released, which is closed, not pooled. MySQL's
`GET_LOCK` locks follow the same rules.
`Queue` is an outbox: `Enqueue` writes through the db.DB the caller passes, so
a job commits or rolls back with the data it is about (tested on Postgres; the
Memory fake has no transactions and says so). Delivery is at least once: a claim
holds a job for a lease, and `Complete`, `Fail` and `Bury` are fenced by the
attempt number — a worker whose lease ran out gets `ErrLeaseLost` and cannot
settle a job another worker now holds. PostgreSQL claims in one statement
(`UPDATE … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED) RETURNING`). MySQL
cannot update a table it selects from in a subquery, and SQLite has no row
locks, so there `queue.SQL` picks due jobs and takes each with an UPDATE whose
WHERE repeats "still due": of two claimers, one changes the row and the other
changes nothing and skips it; when every pick was taken it picks again, so an
empty claim means nothing was due. Times there are Unix nanoseconds (BIGINT),
as the token store keeps them. Tested on all three: the suite, atomicity, and
four claimers over 200 jobs taking each exactly once — which fails, on MySQL
and SQLite, when the UPDATE's "still due" is removed. Plain `FOR UPDATE` would
also take each once on PostgreSQL; `SKIP LOCKED` is there so claimers do not
wait on each other, which no test measures.

`ratelimit` is middleware answering 429 with Retry-After. The client is the
connection's address, or X-Forwarded-For's rightmost untrusted hop when the
connection is from a trusted proxy — never the header's first entry, which the
client writes. Its state is GCRA's one instant per client (when the bucket is
next empty), so the SQL store decides a request with one UPDATE whose WHERE says
"the request fits": the database serialises two at once, and the suite's twenty
concurrent requests through two stores let exactly the burst in (all twenty,
once the WHERE is removed). A store that fails lets the request through and
logs: a limiter that cannot count must not take the API down. Purge deletes
rows whose bucket is full again.

**Not a contract, but ready:** `events` (a typed, in-process bus: `Topic[T]` declared by the publisher,
subscribers run in order inside `Publish`, their errors and panics returned
joined; durable work goes through a subscriber that enqueues a queue job in the
publisher's transaction).

## 9. Database

- `db.DB` (`DBTX`-style: `ExecContext`, `QueryContext`, `QueryRowContext`) is what
  repositories take, so a repository runs the same against a pool or a transaction.
- Beginning a transaction is a separate interface; `db.InTx(ctx, db, fn)` is the
  helper services use.
- **Tests vs `InTx`.** `gessotest` runs each test in a transaction and rolls it back.
  An `InTx` inside that becomes a `SAVEPOINT`, not a nested `BEGIN`. The isolation
  only holds when the server runs in-process (`httptest`); a test against a separate
  process cannot be rolled back.
- **Migrations**: numbered `.up.sql` / `.down.sql`, embedded with `go:embed`, applied
  under a lock so two replicas never migrate at once. Dialects (Postgres, MySQL,
  SQLite) are SQL text plus driver-error translation, so the error mapper reads
  `Constraint` from the driver's error, never a regex.
- **A library's migrations keep their own history.** A gesso package that brings
  tables (`token`) has fixed versions; in the application's history they would
  sort before migrations it already ran and stop `Up`. So each applies through its
  own `Migrate` into its own table (`gesso_token_migrations`).
- **Dates.** `date.Date` is a calendar day — a publication date, a birthday — for
  which `time.Time`, an instant, shifts by a day once a time zone touches it. It
  is "2006-01-02" in JSON, parameters and the database (DATE; text in SQLite),
  `format: date` in OpenAPI; its zero value is null in JSON and NULL in the
  database, so a client that leaves a defaulted date out round-trips as "not
  given" and a NOT NULL column refuses a date nobody gave.
- **Code generation**: typed query functions from annotated SQL. Generated files are
  never edited; `gesso add module` scaffolds once and then the code is the
  application's.

## 10. No `any`

Places it tries to creep in, and the answer for each:

- problem+json extension members: `gesso.Extend(err, T)`, read back in tests with
  `gessotest.Extension[T]`.
- validation parameters: typed rules, not `map[string]any`.
- context values: typed keys and typed accessors (`gesso.IdentityFrom(ctx)`).
- handler output: generic `Out`, never `interface{}`.

## 11. Running with collage

Two processes are first-class; one process is a convenience. Nothing may require a
shared process.

- **Base path** is configuration. Collage's `Handle` passes the full path unchanged,
  so the same app serves at `/api/` when mounted and at `/` standalone, without
  `http.StripPrefix`. The OpenAPI server URL follows it.
- **Lifecycle**: `gesso.Run` handles signals and shuts down in order: HTTP, then
  jobs, then connections. `Start` / `Shutdown` are exported for the mounted case,
  where the **collage-gesso** plugin (github.com/Elagoht/collage-gesso, a module
  of its own so gesso never depends on collage) calls them: `Start` in `OnServe`
  — not `Init`, which a static build and the collage CLI's questions run too, and
  which must not start a queue worker — and `Shutdown` in the plugin's
  `Shutdown`, which collage runs after its server has stopped. The plugin does
  not mount the app: `app.Handle` keeps middleware off an API, a plugin's
  `Host.Handle` does not.
- **A failed Start** leaves no module running, so the app answers every request
  503 (`ErrUnavailable`) from then on, rather than serve handlers whose modules
  are stopped. `Run` returns the error instead.
- **In-process calls: `gesso.NewLocal(app)`** is a `Remote` whose transport is the
  app's `ServeHTTP`. A call is encoded, authenticated, guarded, validated and
  answered exactly as over the network — no second, in-memory code path to keep
  in step — so code written against a `Remote` moves between one process and two
  by changing which Remote it is given.
- **Identity across processes**: collage's login action calls gesso, receives an
  opaque token, keeps it in collage's session, and sends it as
  `Authorization: Bearer` on every server-to-server call. gesso reads bearer
  tokens only — never a cookie, which would make a JSON API forgeable from another
  site (a cross-site form can POST an empty body).
- **Typed client: `Endpoint[In, Out]`**, declared once in a package both sides
  import. The server registers its handler on it (`GetNote.Handle(r, m.get)`) and
  another process calls it (`GetNote.Call(ctx, remote, in, gesso.Bearer(tok))`); the
  compiler holds both to the same types. No code generation: the request is built
  from the same tags the server reads, through the same plan — path values escaped,
  zero query values left out (a pointer sends a zero: a field whose `default` is
  not its zero needs one), only body fields in the body.
  A problem comes back as an `*Error`, so `errors.Is` matches the server's
  declaration across processes and `gesso.FieldErrors` gives a form its fields.

## 12. Phases

1. **v0.1** — router, surfaces, typed handlers, decode (path, query, header, body)
   + validate, problem+json, `DB` / `InTx` with savepoints, driver-error
   classification, migrations, lifecycle, `gessotest`.
2. **v0.2** — guards, identity, opaque tokens, interceptors, OpenAPI, contracts
   (storage, cache, mail, clock, password) + fakes + conformance suites, problem
   extensions (`gesso.Extend`).
3. **v0.3** — live PostgreSQL in the tests, typed client (`Endpoint`), `Locker`,
   `gesso` CLI: `check` (the `ctx` rule), `add module`, `migrate new`.
4. **v0.4** — `Queue` (outbox), SQL code generation (`gesso gen`).
5. **v0.5** — collage bridge: the collage-gesso plugin, `NewLocal`, 503 after a
   failed Start.
6. **v0.6** — from the first trial (a documents API on SQLite): `date.Date`,
   `Config.Authenticator` / `Config.Middleware`, scaffold `text` / `date` /
   update / keyword columns. MySQL verified against a server: `lock.MySQL`,
   `gesso gen :execlastid`, CI on `mysql:8.4`.
7. **v0.7** — `gesso new` and wiring generation (`add module` adds the module
   to the app), `events`, `ratelimit`, more validate rules and `dive`, the queue
   on MySQL and SQLite, the lock discard tested, CI actions on Node 24.
8. **v0.8** — `ratelimit.Store`: Memory and SQL (shared by replicas), GCRA.
   `gesso init` (internal/api in a collage site), the API package named `api`.
9. **v0.9** — what a JSON API meets in production: uploads, CORS, request ids,
   an access log, health probes, a drain.

## 12a. Production surface (v0.9)

What an API needs past its first week, each the smallest thing that does it:

- **PATCH** is `Optional[T]` — three states, absent, null, value — and a
  scaffolded statement that is the same whatever is set: each column
  `CASE WHEN <set> THEN <value> ELSE <column> END`. No SQL is built at run
  time, so nothing a client sends can shape it.
- **Cursor pages** carry the last key, not an offset: a page is as fast deep
  in a table as at its start. A cursor is opaque, not secret: base64 of JSON.
- **Schedules** ride on the queue: a tick is recorded and its job enqueued in
  one transaction, an UPDATE "fired < tick" letting one replica through.
  Missed ticks fire once — a backlog of hourly reports helps nobody. The row is
  created outside the transaction: in it, an insert and an update of one row
  by replicas at once deadlock in MySQL.
- **Idempotency** keys are scoped per client (its credential, else its
  address) and hashed with it; a request's fingerprint is method, URI and
  body. A 5xx or a panic frees the key; a running claim is a lease.
- **ETags** are the hash of the JSON a GET sends, so `ETagOf` computes the
  same value in an update — strong, as If-Match needs.
- **Languages** translate by code: a FieldError says what a size rule counted
  (Unit), so "en az 3 karakter" and "en az 3 öğe" differ as they should.
- **Metrics** label by route pattern, never by path: the series stay as many
  as the routes. No Prometheus or OpenTelemetry dependency: the text format is
  a page of code, and a Tracer is one method an adapter implements.
- **S3 and Redis** with the standard library: SigV4 is checked against AWS's
  worked example and a server that verifies signatures (Zenko CloudServer;
  MinIO's images are no longer public); RESP2 against Redis 7.

## 12b. Security review (2026-10-29)

An independent audit read the code against this document, with proof programs
for what it claimed: no Critical or High findings; six Medium, nine Low, a few
Info, all fixed in v0.9 with a test each (CHANGELOG lists them). What it found
sound is as worth keeping as what it found wrong: all runtime SQL is
parameterized (concatenated pieces are constants, placeholders, a programmer's
table name or counter-made savepoint names); tokens are 32 random bytes with
only their SHA-256 stored; storage cannot leave its root (`os.Root` and
`ValidKey`); the CORS, request-id and X-Forwarded-For handling hold.

Rules the fixes added, which later code keeps:

- **A call stays on its route**: no path value may be `.` or `..` or, in a
  wildcard, hold such a segment or an empty one; no Remote follows a redirect.
- **Another app's words are not this app's**: an error from a Call is
  answered 502 unless the handler maps it.
- **A local call is a request of its own**: it carries no identity from the
  caller's ctx; its address is `ClientAddr`'s, or 127.0.0.1.
- **Memory is bounded where a client chooses the size**: bodies, urlencoded
  forms (1 MiB), multipart memory, rate-limit clients, argon2 hashes at once,
  lock names, readiness checks per second; and **time** is bounded too: the
  server's read, write and idle timeouts.

A second pass re-ran the proofs against the fixes and probed each for
bypasses — encoded dots in path values, a remote error wrapped or joined, the
local-address header sent from outside, IPv4-mapped addresses — and found the
fixes held; it found two issues the fixes made (a readiness cache an aborted
probe could fill with a failure; skipped jobs spending an attempt) and one
gap (a param tag nested in a body struct), all fixed. A released job keeps
its attempt number and gains one allowed attempt, because a number given back
would be reused and stop fencing.

Known and left, in the docs: `storage.Disk` on a case-insensitive filesystem
(macOS) maps `a.png` and `A.png` to one file; a field error from a type's
UnmarshalText repeats that type's message to the client; forms are "simple"
requests a browser sends cross-site without preflight, harmless while gesso
reads bearer tokens only, and never to be paired with a cookie Authenticator; a
proxy in front that decodes %2F before routing could turn an escaped path value
back into a traversal, which Go's mux alone never does; a local call without
ClientAddr comes from 127.0.0.1, so an app that trusts loopback trusts it.

## 13. Decisions

- 2026-10-28 — Name: **gesso**.
- 2026-10-28 — No singletons; constructor injection, wired by hand.
- 2026-10-28 — `ctx` first, everywhere.
- 2026-10-28 — No ORM; SQL → code, not models → SQL.
- 2026-10-28 — Router on `net/http.ServeMux`; no chi.
- 2026-10-28 — Standard library and `golang.org/x/*` free; anything else jointly.
  argon2id comes from `x/crypto`, in the one module (no `gesso/auth` split).
- 2026-10-28 — `golang.org/x/crypto` (argon2) is gesso's first dependency; the
  go directive is 1.26.10, collage's floor.
- 2026-10-28 — Tokens are `gso_` + 32 random bytes; only the SHA-256 is stored;
  every request checks the store, so revocation is immediate. Bearer header only.
  An Authorization header of another scheme is not gesso's and is anonymous.
- 2026-10-28 — **OpenAPI 3.1** is generated from the typed handlers (inputs,
  validate rules, outputs, statuses, guards) when `Config.OpenAPI` is set. It is
  served only where the application says: `OpenAPIConfig.Path` publishes it to
  anyone, `gesso.ServeOpenAPI()` registers it on a router behind its guards (an
  admin surface). A request body is `required` only when one of its fields is,
  since an empty body is accepted as none. The base path goes in `servers`; `{x...}`
  becomes `{x}`; subtree patterns and plain handlers are left out (they declare
  nothing). Generic type names drop their package paths (`Page_Note`), and names
  that collide take their package's (`other_Note`). Any guard marks the route
  bearer-secured with 401/403. Checked once by openapi-spec-validator and
  Redocly's recommended rules: valid, two style warnings (no license, a 204 route
  with no 4xx).
- 2026-10-28 — **Queue times come from the app's clock**, passed into the SQL,
  not the database's `now()`: tests move them. Replicas then rely on synced
  clocks, so a lease must be far longer than any skew between them (default 1m).
  The worker is a gesso module with no routes: it runs where `Start` is called,
  i.e. gesso as its own process. A handler's ctx ends with its lease; a panic is a
  failure; a payload that does not decode is buried at once (`Bury`), not retried;
  a failing `Claim` backs the poll off instead of spinning; `Stop` waits for
  running jobs until its ctx ends. Queue is PostgreSQL only.
- 2026-10-28 — **`gesso gen`: SQL in, typed Go out — not an ORM.** A package's
  `queries.sql` declares its dialect (`-- gesso: dialect postgres`) and each query
  (`-- name: NoteBySlug :one`, `-- in: slug string`, `-- out: Note`); `gen` writes
  `queries.gen.go` with `Queries`/`NewQueries(db.DB)` and one method per query
  (`:one`, `:many`, `:exec`, `:execrows`). The SQL is scanned, not parsed — enough
  to count placeholders (distinct `$n` for Postgres, where `?` is an operator; `?`
  elsewhere) and to name the result columns (the top-level RETURNING list, else the
  first top-level SELECT list), skipping strings, quoted names, comments, `$$`
  bodies and casts. Refused, with `file:line`: `*`, an expression with no AS, a
  column with no field (by `db:"col"`, else snake_case), two columns in one field,
  a placeholder count that does not match `in:`. The package is loaded with the
  generated file blanked, so handwritten code that uses `Queries` loads before the
  file exists. `gen -check` exits 1 on a stale or missing file: the CI hook. Not
  checked: column types and nullability — a NULL into a `string` fails at run time.
  For `?` dialects `in:` follows the order the `?` appear in. Tested by generating
  into a module, vetting, testing, checking, and running every query on SQLite and
  a real PostgreSQL.
- 2026-10-28 — Two processes are acceptable and first-class; one process is not
  required.
- 2026-10-28 — Mockable: consumer-side interfaces, fakes, `gessotest`.

- 2026-10-28 — **Tokens**: opaque, server-side, revocable tokens by default (only
  their SHA-256 is stored). No JWT in gesso; an application that needs a stateless
  token brings one behind `Authenticator`. Lands with guards (v0.2).
- 2026-10-28 — **Collage bridge**: later and optional. Two processes need no bridge.
- 2026-10-28 — **Layout with collage**: one Go module. Collage stays at the root
  (`collage new` lays it out there and `collage dev` builds `.`), gesso's binary is
  `cmd/api`, and both share `internal/`. Collage reserves nothing under `internal/`.
- 2026-10-28 — **Validation tags**: gesso's own validator, using the
  `go-playground/validator` names for the rules it has (`required`, `omitempty`,
  `min`, `max`, `len`, `oneof`, `email`, `url`, `uuid`), so the tags people know
  mean the same thing. It is a subset, not a dependency. A field's name in an error
  is its JSON (or query/path/header) name.
- 2026-10-28 — **Bounds reject, they do not clamp.** `?take=500` against `max=100`
  is a 400 naming the field, not a silent 100. `default:"20"` fills an absent value.
- 2026-10-28 — **Migration versions are timestamps** (`20261028153000_name.up.sql`),
  so modules can each carry their own folder without colliding. Applied migrations
  are checksummed; an edited one stops `Up`. A missing older migration (from a merge)
  stops `Up` unless out-of-order is allowed.
- 2026-10-28 — **Driver errors**: gesso cannot read a driver's error type without
  importing the driver, so the application passes a one-line classifier
  (`db.Violation` from its driver's error); gesso turns violations into 409/400 with
  the field named by an explicit constraint→field map.
- 2026-10-28 — The `ctx`-first check moves to the CLI (`gesso check`, v0.3); v0.1
  enforces it by API shape (every handler and `InTx` takes `ctx`).
- 2026-10-28 — **The CLI is its own module** (`cmd/gesso`), so `golang.org/x/tools`
  never reaches the library's go.mod. `gesso check` reports database/sql calls
  without a context (`Query`, `QueryRow`, `Exec`, `Begin`, `Prepare`, `Ping` on
  `DB`, `Tx`, `Conn`, `Stmt`) and exported methods in `service.go` /
  `repository.go` (or `*_service.go`, `*_repository.go`) whose first parameter is
  not `context.Context`; it exits 1 on findings. `gesso migrate new` only creates
  the timestamped files: applying them needs the application's driver, so up and
  down run in the application's binary through `migrate`. `gesso add module`
  scaffolds a module (endpoints, handler, service with a consumer-side `Store`,
  repository, a fake-store test, migrations) for postgres, sqlite or mysql; the
  scaffold writes no `any`. Field types: string (255, VARCHAR in MySQL), text
  (unbounded), int, bool, float, time, date (`date.Date`). Create, get, list,
  update (PUT, the record whole) and delete. A column named after an SQL keyword
  (`order`, `group`, `user`) is quoted in the SQL it writes, per dialect; MySQL's
  UPDATE counts changed rows, not found ones, so its scaffold asks whether a row
  that changed nothing exists before answering 404. Its tests generate a module,
  vet and test it with `-count=1`, pass it through `check`, and run its
  repository against SQLite, a real PostgreSQL and a real MySQL.
- 2026-10-28 — gesso's own tests that need a database live in a nested
  `integration/` module, so their drivers never reach gesso's `go.mod`: SQLite
  (`modernc.org/sqlite`) and a real PostgreSQL (embedded-postgres, or
  `GESSO_POSTGRES_DSN` in CI) and a real MySQL (`GESSO_MYSQL_DSN`; there is no
  embedded one). Each Postgres or MySQL test gets a database of its own;
  without a server they skip, saying "POSTGRES UNVERIFIED" / "MYSQL
  UNVERIFIED", and CI fails on either.
- 2026-10-28 — **`gesso new` writes the wiring, `add module` keeps it.** A new
  project is `main.go` (settings from ADDR, BASE_PATH, DATABASE_URL) and
  `internal/api/api.go`, which opens the database for its dialect (SQLite with
  foreign keys, WAL and a busy timeout; MySQL with multiStatements and
  parseTime), runs every module's migrations as one history, maps the driver's
  violations, serves OpenAPI at /openapi.json and closes the database at
  shutdown; `api_test.go` starts it. `app.go` declares `modules`, `migrations`
  and `var dialect = migrate.X`: `add module` reads the dialect from it, inserts
  the module into both lists before their closing braces (the rest of the file
  untouched) and adds the import. Not a generator that owns the file: the code
  is the application's, and a list `add module` cannot find is reported, for
  the user to wire by hand. The package is `api` (internal/api), not `app`:
  in a collage site — `gesso init` writes only it, into the existing module —
  collage's newApp already has an `app`. Tried end to end: collage new, gesso
  init, add module, collage plugin add elagoht/gesso, a page reading the API
  through NewLocal; a note created over /api showed on the page, in one
  process. A migration's version is the first second, from
  now, that no migration in the project has: two modules scaffolded in one
  second used to collide in the one history.
- 2026-10-28 — **validate's rules**: go-playground's names (gt, gte, lt, lte, eq,
  ne, ip, ipv4, ipv6, hostname, alpha, alphanum, numeric, lowercase, uppercase,
  contains, excludes, startswith, endswith, unique, dive), plus gesso's `slug`.
  No regex rule: a pattern with a comma breaks the tag, and a SelfValidator says
  it in Go. OpenAPI carries each: pattern (the first only — a schema has one),
  exclusiveMinimum/Maximum, uniqueItems, formats, and the rules after dive on
  `items`.
- 2026-10-28 — **MySQL verified** (8.4) by every suite PostgreSQL runs:
  migrations with GET_LOCK under contention, InTx and savepoints, the token
  store, the classifier through db.Errors, `lock.MySQL` (GET_LOCK, one
  connection per held lock, like `lock.Postgres`), DATE and DATETIME(6), the
  scaffold, and `gesso gen`. What it took: the DSN needs `multiStatements=true`
  (a migration file is one Exec) and `parseTime=true` (DATETIME into
  time.Time; `date.Date` scans either way); MySQL has no RETURNING, so
  `gesso gen` has `:execlastid`; and **DDL is not transactional** — MySQL
  commits each CREATE, ALTER and DROP by itself, so a migration that fails
  after one leaves it applied and unrecorded. The rows a failed migration wrote
  are still rolled back. Keep one DDL statement per MySQL migration. The queue
  runs on all three (see §8).
- 2026-10-28 — **Each request in a test transaction runs in a savepoint.**
  PostgreSQL fails a whole transaction at its first error, so a test that expected
  a 409 could send nothing after it. `gessotest.Tx` returns a `TxDB`, and a Client
  of the same test wraps every request in a savepoint, rolled back when the request
  left the transaction failed.
- 2026-10-28 — `db.Transactor` lets a DB run a unit its own way; InTx defers to
  it first. `gessotest.FakeDB` is one, so a service that uses transactions is
  unit-tested with fake repositories and no database.

## 14. The v1 surface

Sealed on 2026-10-29, after the security review: from v1.0.0 these change only
in a major version.

- **gesso**: `New`, `Config` (and `CORS`, `Health`, `OpenAPIConfig`), `App`
  (`ServeHTTP`, `Run`, `Start`, `Shutdown`, `OnShutdown`, `OpenAPI`), `Module`,
  `Starter`, `Stopper`, `Router` (`Use`, `Authenticate`, `Guard`, `Intercept`,
  `Group`, `Handle`, the method helpers), `Handle`, `HandlerFunc` and its
  options (`Status`, `Guards`, `MaxBodyBytes`, `Summary`, `Description`,
  `Tags`), `Empty`, `File`/`NewFile`, `Endpoint`, `Remote`/`NewRemote`/
  `NewLocal`, the call options (`Bearer`, `Header`, `ClientAddr`), `Error` and
  its constructors and `Err*` declarations, `Extend`, `Problem`, `FieldErrors`,
  `WriteProblem`, `ServeOpenAPI`, `ErrorMapper`, `Identity`, `IdentityFrom`, `WithIdentity`,
  `Authenticator`, `Guard`, `RequireIdentity`, `RequireRole`, `Interceptor`,
  `Call`, `Middleware`, `Request`, `ResponseHeader`, `RequestIDFrom`,
  `Optional`/`Some`/`Null`, `Page`/`EncodeCursor`/`DecodeCursor`,
  `Download`, `Stream`/`SSE`, `Metrics`, `Tracer`, `Language`/`Turkish`,
  `IfMatch`/`ETagOf`.
- The input tags and their meaning: `path`, `query`, `header`, `json`, `form`,
  `default`, `accept`, `maxsize`, `validate` with the rules §7 and validate's
  doc list.
- **Packages**: `validate`, `db`, `migrate`, `date`, `clock`, `password`,
  `storage`, `storage/s3`, `cache`, `cache/redis`, `mail`, `token`, `lock`,
  `queue`, `events`, `ratelimit`, `idempotency`, `gessotest`, and every
  `*test` suite: their exported names as of v0.9.
- **Wire formats**: problem+json's members and codes, the token format
  (`gso_` + 43 base64url characters), the tables gesso's own migrations create
  (`gesso_tokens`, `gesso_jobs`, `gesso_schedule`, `gesso_ratelimit`,
  `gesso_idempotency` and their history tables) —
  a later change to one ships as a new migration, never an edited one.
- **The CLI**: its commands and flags, and that a scaffold, once written, is the
  application's: a later gesso never edits it except where `add module` wires a
  module into `internal/api/api.go`.

Not sealed: what the OpenAPI document says beyond being valid OpenAPI 3.1
describing the routes (it may describe more), log messages and their attributes,
and anything unexported.

## 15. Open questions

- **Events across processes** are not `events`' job: it is in-process, and
  across processes the queue is the transport — a subscriber enqueues in the
  publisher's transaction, a worker in the other process handles.
- **CI** (`.github/workflows/ci.yml`): library (gofmt, vet, `-race`),
  integration against `postgres:18` and `mysql:8.4` services through
  `GESSO_POSTGRES_DSN` and `GESSO_MYSQL_DSN`, the CLI's generate-and-run tests
  (with the MySQL service too) — a database test that skips there fails the job
  — and lint (staticcheck v0.7.0, govulncheck).
- **Releasing.** gesso is tagged `vX.Y.Z`, the CLI `cmd/gesso/vX.Y.Z` (its own
  module, which imports nothing from gesso, so it has no `replace` and
  `go install github.com/Elagoht/gesso/cmd/gesso@latest` works). `integration/`
  keeps its `replace ../`: it tests the tree, and is never installed.
