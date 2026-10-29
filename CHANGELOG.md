# Changelog

gesso is before 1.0: a minor release may change the API. Each entry says what to
change.

## v0.9.2 — 2026-10-29

- **`gesso new` pins gesso**: the go.mod it writes requires the gesso release
  the CLI was installed as (`cmd/gesso@v0.9.2` → `gesso v0.9.2`). `go mod tidy`
  used to choose, and for a while after a release the module proxy still lists
  the one before as latest: a new project got gesso v0.8.0 and did not build.
  `gesso init` prints the `go get` for the same version.

## v0.9.1 — 2026-10-29

As collage does it:

- **`gesso.Main(api.New)`**: a program's whole `main`. It reads the working
  directory's `.env` — `.env.development`, or `.env`, under `gesso dev` — a
  variable the environment sets winning; hands `api.New` a `gesso.Env` (Dev,
  Addr from `-addr`/`ADDR`, a Logger); serves until SIGTERM, or runs the app's
  command named on the command line: `app migrate status`.
  `gesso.LoadEnvFile` for a program of its own making.
- **`App.Command`** registers such a command; **`migrate.Command`** is the
  migrate one (status, up, down [n]).
- **`gesso dev`**: builds and runs the program in development (`GESSO_DEV=1`),
  rebuilding and restarting it on changes to `.go`, `.sql`, `go.mod` and the
  environment files; a failed build keeps the running version.
- `gesso new`: `main.go` is `gesso.Main(api.New)`; `api.New(env)` reads
  `DATABASE_URL` and `BASE_PATH`, `api.NewWith(cfg)` takes a config (a collage
  site, a test); migrations run when the app starts, not when it is built, so
  `migrate status` sees the database as it is. `.env` is ignored in git.
- **`gesso add module` names and places types as `Req…` and `Res…`**: a
  module's `models` package holds `models.go` (the stored record, with
  `Response()`), `requests.go` (`ReqCreateDoc`, `ReqUpdateDoc`, `ReqPatchDoc`,
  `ReqListDocs`, `ReqDocID`) and `responses.go` (`ResDoc`); the endpoints are
  in `endpoints.go`. *Change:* modules scaffolded before keep their `…In`
  types; nothing rewrites them.

## v0.9.0 — 2026-10-29

- **mail.SMTP**: STARTTLS by default (implicit TLS on 465; plain only to this
  machine), AUTH PLAIN, text and HTML as multipart/alternative, Turkish
  subjects encoded, Bcc in no header, line breaks in addresses and subjects
  refused.
- **Scheduled jobs**: `queue.Every` and `queue.Cron` enqueue a job once per tick
  across replicas (`Queue.Fire`, the `gesso_schedule` table); missed ticks fire
  once. *Change:* `Queue` has `Fire` and `Release`.
- **PATCH**: `gesso.Optional[T]` tells a field left out from null from a value;
  rules apply to the value, `notnull` refuses null; `gesso.Some`, `gesso.Null`.
  The scaffold adds a PATCH endpoint, one fixed statement per module.
- **Cursor pages**: `gesso.Page[T]`, `EncodeCursor`, `DecodeCursor`; the
  scaffold lists `?after=…&take=…`. *Change:* scaffolded lists were take/skip.
- **Files and streams**: a handler answers `*gesso.Download` (ranges and
  If-Modified-Since for a seekable body) or `gesso.SSE` events.
- **`gesso new`'s migrate command**: `app migrate status | up | down [n]`, and
  `api.Migrator`.
- **Metrics and tracing**: `Config.Metrics` (Prometheus text, by route
  pattern), `Config.Tracer` (any tracer, OpenTelemetry through a few lines).
- **Idempotency-Key**: the `idempotency` package, memory and SQL stores.
- **ETags**: GET answers carry one, If-None-Match gets 304; `gesso.IfMatch`
  and `gesso.ETagOf` give updates optimistic concurrency (412).
- **Languages**: `Config.Languages` translates field errors and problem
  details by Accept-Language; `gesso.Turkish` built in.
- **storage/s3** and **cache/redis**, with the standard library only.
- **Events across processes**: `queue.Publish` and `queue.Deliver` carry an
  `events.Topic` through the queue, in the publisher's transaction.
- `storage.Disk` holds keys to their exact spelling on a case-folding file
  system (`ErrKeyCollides`); `Config.FormMemory`; a type's parse message is cut
  at 100 characters.

- **Uploads**: `form` tags read `multipart/form-data` (or a urlencoded form):
  `*gesso.File` and `[]*gesso.File`, typed by their first bytes, held to
  `accept` and `maxsize` tags; `gesso.MaxBody(n)` per route; `gesso.NewFile` for
  `Endpoint.Call`, which sends forms; OpenAPI describes them.
- **CORS**: `Config.CORS` answers preflights for routes that exist and marks the
  answers for the origins allowed.
- **Request ids**: every answer has `X-Request-ID`, failures are logged with it,
  `gesso.RequestIDFrom(ctx)`; `Config.TrustRequestID` keeps a proxy's.
- **`Config.AccessLog`**: a line per request.
- **`Config.Health`**: `/healthz` and `/readyz` with checks; readiness turns 503
  while the app stops. **`Config.DrainDelay`** keeps serving that long after
  SIGTERM.
- `gesso new` turns on the access log and health, with a database check.
- **Security review** (an independent audit; no Critical or High findings),
  each fixed and tested:
  - `Endpoint.Call` refuses a path value that would leave its route (`..`,
    `.`, an empty wildcard segment), and a Remote follows no redirect — a
    redirect would carry the call's credential to another route.
  - An error another app answered a `Call` with, returned from a handler as it
    is, is answered 502 `bad_gateway`: its detail (a DSN, a path) and its 401
    are not passed to the client. `errors.Is` still matches it in code.
  - `gesso.ClientAddr(ip)`: whom a call is made for — `NewLocal` takes it as
    the request's address, a Remote sends it as X-Forwarded-For — so a rate
    limit counts end clients, not the caller. A local call no longer carries
    the caller's ctx identity: only what it sends authenticates it.
  - `Run` sets ReadTimeout, WriteTimeout (1m) and IdleTimeout (2m)
    (`Config.ReadTimeout` …): a client trickling bytes cannot hold a
    connection for ever.
  - A urlencoded form is read only for an input with no files, and at most
    1 MiB of it, whatever the route's `MaxBodyBytes`.
  - An input that embeds a *pointer* to a struct with path, query, header or
    form fields is refused at New: the body could fill them.
  - ratelimit counts an IPv6 client by its /64; `Memory` forgets one client in
    constant time at its bound.
  - A Bearer header with no single token is an invalid credential, not none.
  - `/readyz` answers a result again for `Health.CacheFor` (1s).
  - `password.Argon2id.Concurrent` bounds hashes at once (default: CPUs).
  - The queue worker's lease runs from the batch's claim; a job whose lease
    ran out on its last attempt is dead, not taken again for ever.
  - `gesso new` with MySQL runs migrations on a connection of their own:
    the app's pool takes one statement per Exec.
  - Scaffolds say that writes are open and OpenAPI is public until guarded.
  - Success answers carry `X-Content-Type-Options: nosniff`; `Extend` refuses
    standard members in any case; `lock.Memory` forgets names nobody holds.
  - A second pass, re-running the proofs on the fixes, found two issues they
    made and one gap, fixed too: readiness checks no longer run on the
    prober's ctx (a hung-up probe left a failure cached: anyone could fail
    every replica's readiness); a path, query, header or form tag anywhere
    inside a body field is refused at New, not only in an embedded pointer;
    jobs a worker skips at its batch's deadline are handed back with
    **`Queue.Release`** (*Change:* a new method of the interface) rather than
    spending an attempt they never ran; `ClientAddr` unmaps IPv4-mapped
    addresses; a multipart form with no file is bounded like a urlencoded
    one. A third pass closed the last corner (an unexported embedded struct
    with a json tag) and logs a skipped job lost to a handler that outran its
    lease.
- *Change, before v1:* names made consistent across packages —
  `token.MemoryStore`/`SQLStore` are `token.Memory`/`token.SQL`
  (`NewMemory`, `NewSQL`), as in queue, ratelimit, cache and storage;
  `token.Migrations` and the new `ratelimit.Migrations` are functions of the
  dialect, as `queue.Migrations` is; the `queue.Postgres` alias is gone
  (`queue.SQL`); the route option `MaxBody` is `MaxBodyBytes`, as the Config
  field is.

## v0.8.0 — 2026-10-29

- **Rate limits shared by replicas**: `ratelimit.Store`, with `NewMemory(max)`
  (per process, as before) and `NewSQL(db, dialect)` (the gesso_ratelimit
  table, `ratelimit.Migrate`; `Purge` deletes rows whose bucket is full again),
  held to `ratelimittest.Run`. The algorithm is GCRA: the same limits, one
  instant per client.
- *Change:* `Limiter.Allow(key)` is `Allow(ctx, key)` and returns an error too;
  `Options.MaxClients` is `Options.Store: ratelimit.NewMemory(max)`.
- A store that fails lets the request through and logs (`Options.Logger`).
- **`gesso init`**: `internal/api` in a module that exists — a collage site
  that serves its API in the same process — with the steps to wire it.
- *Change:* `gesso new` writes the API as `internal/api` (package `api`), not
  `internal/app`, so it sits beside collage's `app` without a rename. A project
  from v0.7.0 keeps working; `add module` wires into `internal/api/api.go` only,
  so rename the folder and package to have it wire yours.
- The API refuses an empty `Config.Database` instead of opening a database of
  its own.

## v0.7.0 — 2026-10-29

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
