# gesso — design

Status: **design, no code yet** (2026-10-28).

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

1. **v0.1** — router, surfaces, typed handlers, decode + validate, problem+json,
   `ctx` rule, `DB` / `InTx`, migrations, lifecycle, `gessotest`.
2. **v0.2** — guards, interceptors, query structs, OpenAPI, contracts + fakes +
   conformance suites.
3. **v0.3** — SQL code generation, `gesso add module`, typed client.
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

## 14. Open questions

- **Tokens.** Self-written JWT (HS256 / EdDSA only, strict) vs opaque, revocable
  tokens stored server-side. Recommendation: opaque by default; JWT only if a
  stateless consumer needs it.
- **Collage bridge.** A `collage-gesso` plugin for the one-process case (mount,
  shutdown, identity). Recommendation: later, optional.
- **Project layout with collage.** One Go module, collage at the root as
  `collage new` lays it out, gesso under `cmd/api`, sharing `internal/` types — to be
  checked against `collage new` and `collage dev` before it is fixed.
- **Validation tags.** Own tag syntax vs compatibility with
  `go-playground/validator` tags, which people already know.
