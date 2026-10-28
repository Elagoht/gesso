# gesso — design

Status: **v0.1 implemented, not released** (2026-10-28). §12 lists the phases.

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
    DB      gesso.DB
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
middleware → guard → decode + validate → interceptor → handler → error mapper
```

- **Middleware**: plain `func(http.Handler) http.Handler`.
- **Guard**: decides whether the request may proceed (identity, role).
- **Decode + validate**: path, query and body into the handler's input type.
  Query structs are declarative: `query:"take" default:"20" max:"50"`.
- **Interceptor**: wraps the handler (timing, transactions, response shaping).
- **Error mapper**: typed errors to problem+json. Field names in validation errors
  are the JSON names, not the Go names; messages are i18n keys.

**Surfaces** are first-class: a group of routes under a prefix with its own
middleware stack (an app API, an admin API, a partner API with signed requests).

## 8. Contracts and implementations

gesso defines an interface only when (a) gesso itself consumes it, or (b) several real
implementations are usual. Everything else the application defines, consumer-side.

| Contract | Reference implementation (in gesso) | Fake (in gesso) |
|---|---|---|
| `Storage` | local disk | memory |
| `Cache` | memory | memory |
| `Mailer` | log | capture |
| `Clock` | system | fixed / advanceable |
| `Hasher` | argon2id (`x/crypto`) | — |
| `Locker` | Postgres advisory lock | memory |
| `Queue` | database outbox | memory |

Every contract ships a **conformance suite**, e.g. `storagetest.Run(t, impl)`, which
any implementation, the application's own included, must pass.

## 9. Database

- `gesso.DB` (`DBTX`-style: `ExecContext`, `QueryContext`, `QueryRowContext`) is what
  repositories take, so a repository runs the same against a pool or a transaction.
- Beginning a transaction is a separate interface; `gesso.InTx(ctx, db, fn)` is the
  helper services use.
- **Tests vs `InTx`.** `gessotest` runs each test in a transaction and rolls it back.
  An `InTx` inside that becomes a `SAVEPOINT`, not a nested `BEGIN`. The isolation
  only holds when the server runs in-process (`httptest`); a test against a separate
  process cannot be rolled back.
- **Migrations**: numbered `.up.sql` / `.down.sql`, embedded with `go:embed`, applied
  under a lock so two replicas never migrate at once. Dialects (Postgres, MySQL,
  SQLite) are SQL text plus driver-error translation, so the error mapper reads
  `Constraint` from the driver's error, never a regex.
- **Code generation**: typed query functions from annotated SQL. Generated files are
  never edited; `gesso add module` scaffolds once and then the code is the
  application's.

## 10. No `any`

Places it tries to creep in, and the answer for each:

- problem+json extension fields: typed per error, `Problem[T]`.
- validation parameters: typed rules, not `map[string]any`.
- context values: typed keys and typed accessors (`gesso.Identity(ctx)`).
- handler output: generic `Out`, never `interface{}`.

## 11. Running with collage

Two processes are first-class; one process is a convenience. Nothing may require a
shared process.

- **Base path** is configuration. Collage's `Handle` passes the full path unchanged,
  so the same app serves at `/api/` when mounted and at `/` standalone, without
  `http.StripPrefix`. The OpenAPI server URL follows it.
- **Lifecycle**: `gesso.Run` handles signals and shuts down in order: HTTP, then
  jobs, then connections. `Start` / `Shutdown` are exported for the mounted case.
- **Identity**: an `Authenticator` interface behind `gesso.Identity(ctx)`, so where
  the identity comes from (a token, a signed header from collage, a session) is the
  application's choice.
- **Typed client**: generated from the same routes, for collage (or anything) to call
  gesso over HTTP.

## 12. Phases

1. **v0.1** — router, surfaces, typed handlers, decode (path, query, header, body)
   + validate, problem+json, `DB` / `InTx` with savepoints, driver-error
   classification, migrations, lifecycle, `gessotest`.
2. **v0.2** — guards, identity, opaque tokens, interceptors, OpenAPI, contracts +
   fakes + conformance suites, typed problem extensions (`Problem[T]`).
3. **v0.3** — `gesso` CLI: `check` (the `ctx` rule), SQL code generation,
   `add module`, `migrate`, typed client.
4. **later** — event bus, collage bridge, wiring generation.

## 13. Decisions

- 2026-10-28 — Name: **gesso**.
- 2026-10-28 — No singletons; constructor injection, wired by hand.
- 2026-10-28 — `ctx` first, everywhere.
- 2026-10-28 — No ORM; SQL → code, not models → SQL.
- 2026-10-28 — Router on `net/http.ServeMux`; no chi.
- 2026-10-28 — Standard library and `golang.org/x/*` free; anything else jointly.
  argon2id comes from `x/crypto`, in the one module (no `gesso/auth` split).
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
- 2026-10-28 — gesso's own tests that need a database run against SQLite (pure Go,
  `modernc.org/sqlite`) in a nested `integration/` module, so gesso's `go.mod` stays
  empty; Postgres-specific tests run when `GESSO_POSTGRES_DSN` is set.

## 14. Open questions

None yet.
