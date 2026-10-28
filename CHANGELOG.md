# Changelog

gesso is before 1.0: a minor release may change the API. Each entry says what to
change.

## v0.7.0 (unreleased)

- **`gesso new <module path> [-dialect sqlite|postgres|mysql]`**: a project
  with `main.go` (ADDR, BASE_PATH, DATABASE_URL), `internal/app/app.go` (opens
  the database with the dialect's settings, runs every module's migrations,
  maps violations, serves `/openapi.json`, closes the database at shutdown) and
  a test that starts it.
- **`gesso add module` wires the module** into a `gesso new` app: it adds it
  to `modules` and `migrations` in `internal/app/app.go`, with the import, and
  takes the dialect from there.
- Migration versions are unique across the project: two modules scaffolded in
  one second no longer collide.
- **`queue` on MySQL and SQLite**: `queue.NewMySQL`, `queue.NewSQLite`, and the
  type `queue.SQL` (`queue.Postgres` is an alias). *Change:*
  `queue.Migrate(ctx, d)` is `queue.Migrate(ctx, d, migrate.Postgres)`, and
  `queue.Migrations` is a function of the dialect.
- **`events`**: a typed, in-process event bus between modules.
- **`ratelimit`**: a token bucket per client, as middleware answering 429 with
  Retry-After; `gesso.ErrTooManyRequests` and `gesso.WriteProblem` for
  middleware that answers.
- **validate**: `gt`, `gte`, `lt`, `lte`, `eq`, `ne`, `ip`, `ipv4`, `ipv6`,
  `hostname`, `alpha`, `alphanum`, `numeric`, `lowercase`, `uppercase`,
  `contains`, `excludes`, `startswith`, `endswith`, `unique`, `dive`, and
  gesso's `slug`; each in the OpenAPI schema too (`pattern`,
  `exclusiveMinimum`, `uniqueItems`, formats, rules after dive on `items`).
- The lock's handling of a connection that cannot release its lock is tested.
- CI: actions/checkout and setup-go v7 (Node 24).
- A guide in `docs/`.

## v0.6.0 — 2026-10-28

- `date.Date`, a calendar day: "2006-01-02" in JSON, parameters and the
  database; `format: date` in OpenAPI; zero is null / NULL.
- `Config.Authenticator` and `Config.Middleware` reach every module.
- `gesso add module`: `text` and `date` fields, an update endpoint (PUT),
  columns named after SQL keywords quoted.
- MySQL verified against a server: `lock.MySQL`, `gesso gen :execlastid`, CI on
  MySQL 8.4.

## v0.5.0 — 2026-10-28

- `gesso.NewLocal(app)`: an endpoint call in the same process, through the
  whole pipeline.
- An app whose Start failed answers every request 503 `unavailable`.
- The collage-gesso plugin (its own repository) runs an app's lifecycle in
  collage.
- `go install github.com/Elagoht/gesso/cmd/gesso@latest` works.

## v0.1 – v0.4 (untagged)

Router and typed handlers, validation, problem+json, `db` and `InTx`,
migrations, `gessotest`; guards, bearer tokens, interceptors, OpenAPI,
contracts with fakes and suites; PostgreSQL in the tests, `Endpoint`, `lock`,
the CLI (`check`, `add module`, `migrate new`); the queue and `gesso gen`.
