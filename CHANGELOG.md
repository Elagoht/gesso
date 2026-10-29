# Changelog

gesso is before 1.0: a minor release may change the API. Each entry says what to
change.

## v0.13.0 — 2026-10-29

The model writes the module, and the app writes almost nothing. This breaks
v0.12 apps: make the project again with `gesso new`, `gesso add auth` and
`gesso add module`, move the model's fields over, and keep your own code as
hooks.

- **`main.go` makes the app, `register.go` registers**: `gesso.Config` takes
  `Env` (Main's address, logger and `BASE_PATH`) and `Database`
  (`gesso/sqlite`, `gesso/postgres`, `gesso/mysql`: `sqlite.Open(path)`); the
  app opens it, migrates it as it starts, closes it, answers unique violations
  409 on their field, and adds `migrate`. `register.go` is
  `app.Register(auth.New(), docs.New())`, with `app.Command` and `app.Use`
  lines from `gesso add command|middleware`. `internal/api/api.go`, `Deps`,
  `newDeps` and the lists are gone (`gesso init`, for a collage site, still
  writes `internal/api`: `api.New(url, basePath)`).
- **Modules set themselves up**: `gesso.Initializer` (`Init(app)`), with
  `App.DB`, `App.Dialect`, `App.Migrations`, `App.Schema`, `App.Constraints`,
  `App.Command`, `App.Use`, `gesso.ModuleOf`. A registered module that is an
  Authenticator is the app's, found per request, so registration order does
  not matter.
- **Short routes**: `gesso.Get(r, "/docs/{slug}", m.get, opts...)` (and
  `Post`, `Put`, `Patch`, `Delete`) take In and Out from the handler, and
  return the Endpoint. `gesso.Allowed(ctx, guard)`.
- **`auth.New()`** takes no options on an app with a database: users in a
  table, its migrations, the `users` command, the authenticator.
  `auth.New(auth.Options{…})` for anything else. `gesso.Identity.Name` is the
  username, read again each request with the roles.
- **`gesso gen` writes the whole module**: `module.gen.go` (the module, its
  routes, handlers and Endpoints) beside `repository.gen.go` and the requests
  and responses. `add module` writes only `models/models.go`; `endpoints.go`,
  `handler.go` and `service.go` are gone. Your code goes in hooks gen finds by
  name on `*Module`: `BeforeCreate`, `BeforeUpdate` (in a transaction, on the
  merged record), `BeforeDelete`, and `routes` for routes of your own, where
  `gesso add endpoint` writes.
- **The model says more**:
  - `//gesso:sortable`: records in the client's order; new ones last;
    `PUT /<table>/order` with `{"keys": […]}` reorders what the caller sees.
  - `//gesso:write <guard>`, `//gesso:read <guard>` (`add module -guard`,
    `-read`, `-sortable`).
  - `listable`: the list answers these fields (`ResXItem`), a record all.
  - `filterable`: `?author=ada`. `orderable`: `?order=title`, `?order=-title`.
  - `guard=<guard>`: a field only the guard's callers see and write; a PUT by
    others keeps it.
  - `gate=<guard>` on a bool: records with it false exist only for the guard's
    callers.
  - An optional time or date is a pointer.
- `gesso new` has no `/healthz` until `Config.Health` is set; its test starts
  the app and asks for `/openapi.json`.

## v0.12.0 — 2026-10-29

- **Swagger UI**: `OpenAPIConfig.UI` serves it at a path, `gesso.ServeSwaggerUI()`
  behind guards. `gesso new` turns it on at `/swagger`; an app from v0.11 adds
  `UI: "/swagger"` to its `OpenAPIConfig`.
- **Fix**: the OpenAPI document left out `components.securitySchemes` when every
  guard was given to `Handle` (`gesso.Guards(...)`), not to a router — the
  operations named a `bearer` scheme that did not exist.
- **OpenAPI operations are tagged by their module** (`auth`, `docs` …) unless
  `gesso.Tags` says otherwise, and carry no summary unless `gesso.Summary` gives
  one — it used to repeat the method and path.
- **No made-up module names in the CLI**: `gesso new`, `add auth` and help say
  `<name>`, `<field>:<type>`. `add module -dialect` shows its real default.

## v0.11.0 — 2026-10-29

- **`gesso add auth` is a users table, nothing else**: no `-users` flag, no
  `AUTH_USERNAME`/`AUTH_PASSWORD` — one user from settings was an app's need,
  not the framework's. `auth.NewSingle` stays in the library.
- **`register.go`**: the lists `gesso add` writes to — `Deps`/`newDeps`,
  `authenticator`, `modules`, `appMiddleware`, `migrations`, `stores`,
  `appCommands`, `constraints` — moved out of `api.go`, which is setup only and much shorter.
  Generated files carry far fewer comments. An app from v0.10 moves its lists
  by hand, or is regenerated.
- **The model is the source**: `models/models.go` holds a `//gesso:table`
  struct with `gesso:"…"` tags; `gesso gen` writes `models/requests.gen.go`,
  `models/responses.gen.go` and `repository.gen.go` from it, and a migration
  for what changed since `models/schema.gen.json` — added columns, indexes,
  uniques; a dropped column only with `-drop`, a type change never
  (`-no-migration` after your own). Generated requests are `ReqCreateX`,
  `ReqUpdateX`/`ReqPatchX` (a `Key` from the path), `ReqXKey`, `ReqListXs`;
  the repository's methods `Insert`, `ByKey`, `List`, `Update`, `Patch`,
  `Delete`, with `Constraints` for register.go.
- **`-field name:type[:options and rules]`** on `add module`: `key` (the URL
  names records by it), `unique` (a 409 on the field, every dialect),
  `index`, `optional`, `sort`, and validation rules checked when the module is
  made. `-fields` stays, the short form.
- **`gesso version`** (`-v`, `--version`): the CLI's release — the gesso
  `gesso new` pins — or a checkout's commit, and the Go it was built with.
- **What collage's CLI had and gesso's did not**: `gesso add endpoint
  <module>/<name>` (Endpoint, handler, Req/Res, the Routes line), `add
  command`, `add middleware` (register.go's `appCommands`, `appMiddleware`),
  `gesso build` (static binary, `-os`/`-arch`, `-i` for a Dockerfile and a
  systemd unit), `gesso routes`, `gesso inspect`, `gesso check` with stale
  generated files and unguarded writes and `-json`, `gesso help <command>`,
  "did you mean", exit codes 0/1/2, `gesso completion bash|zsh|fish`,
  `gesso man`, `gesso ai init` (AGENTS.md), `gesso new -template demo -force
  -ai` and `.env.example`. docs/cli.md.
- **`App.Routes`**, and `gesso.Main` answers `gesso-inspect` with the app as
  JSON (`Inspection`), serving nothing.
- **Logs for a person on a terminal** from `gesso.Main`; `gesso.TerminalLogger`.
- **`gessotest.Identities`** (an Authenticator for tests) and
  **`gessotest.SignIn`**.
- **Guards as in collage**: `gesso add guard <name>` writes
  `internal/guards/<name>.go`; `gesso add module … -guard <name>` guards the
  writes and refuses a guard that does not exist. `add auth` writes
  `signedIn`.

## v0.10.0 — 2026-10-29

Sign-in, ready to use, and modules that are not CRUD:

- **`auth` package**: a module serving `POST /auth/login`, `POST /auth/logout`,
  `GET /auth/me` and `PUT /auth/password`, issuing package token's bearer
  tokens, and the app's `Authenticator`. Who signs in is an `auth.Users`:
  `NewSingle` (one user from settings), `NewSQL` (the `gesso_users` table,
  argon2id, rehashed at sign-in, a `users` command: add, password, roles,
  delete, list), or your own; `auth/authtest` is the contract. Attempts are
  limited per address and username, and per address. `auth.Migrate` makes
  `gesso_tokens` and `gesso_users`.
- **`gesso add auth [-users single|sql]`** wires it into `internal/api/api.go`,
  and the test's Config; nothing of it is copied in.
- **`gesso add module <name> -empty`**: `module.go`, `endpoints.go` and the
  `models/` files, no table, no migration.
- **`gesso new`'s api.go** has `stores()`: gesso's own tables — tokens, users,
  a queue — made after the modules' migrations, on their connection, each in
  a history of its own. `migrator` returns that connection rather than its
  Close. An api.go from before has no `stores`: `add auth` refuses it, and
  says what to add by hand.

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
