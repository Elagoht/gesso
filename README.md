# gesso

A Go backend framework for JSON APIs, made to sit under
[collage](https://github.com/Elagoht/collage) — or anything else — and to need
nothing from it. The standard library and `golang.org/x/crypto`; a database's
driver comes with `gesso/sqlite`, `gesso/postgres` or `gesso/mysql`.

- **Modules from a model**: a struct with `gesso` tags is the table, its
  migrations and its endpoints; `gesso gen` keeps them in step. No
  singletons, no container.
- **Typed handlers**: `func(ctx, In) (Out, error)`; input decoded from path, query,
  header and body, validated before your code runs.
- **Guards and bearer tokens**: opaque, revocable, only their hash stored.
- **OpenAPI 3.1** generated from the handlers' types and rules — it cannot drift.
- **problem+json** for every error, the router's 404 and 405 included.
- **Transactions that nest**: `db.InTx` over `*sql.DB`, or as a savepoint inside a
  test's transaction.
- **Migrations** per module, timestamped, checksummed, locked.
- **PostgreSQL, MySQL and SQLite**: `Config.Database` opens it, migrates it
  as the app starts and closes it; each verified against a real server in CI;
  no ORM.
- **Calendar dates**: `date.Date` is "2026-10-28" in JSON, parameters and the
  database — no time zone to shift it by a day.
- **An outbox queue** on PostgreSQL, MySQL or SQLite: jobs commit with the data
  they are about; at-least-once, leased, fenced.
- **Events** between modules, typed, in process; **rate limits** per client,
  shared by replicas through the database.
- **Uploads** typed like the rest (`*gesso.File`, type read from the bytes),
  downloads with ranges, server-sent events, **PATCH** (`Optional[T]`),
  cursor pages, ETags, idempotency keys.
- **Running it**: CORS, request ids, an access log, Prometheus metrics, a
  tracer hook, health probes, a graceful drain, answers in the client's
  language.
- **S3, Redis and SMTP**, with the standard library only; scheduled jobs on
  the queue.
- **Validation** with go-playground's rule names — `gt`, `slug`, `unique`,
  `dive` and the rest — written into the OpenAPI schema too.
- **Contracts with fakes**: storage, cache, mail, clock, password hashing, locks — each
  fake held to the same conformance suite as the real implementation.
- **gessotest**: in-process client, problem assertions, rolled-back test
  transactions.

```go
//gesso:table notes
//gesso:write signedIn
type Note struct {
	ID    int64
	Title string `gesso:"listable,max=200"`
	Slug  string `gesso:"key,slug,max=100"`
	Body  string `gesso:"text"`
}
```

`gesso gen` writes the notes module from it: `POST /notes`, `GET /notes`,
`GET /notes/{slug}`, `PUT`, `PATCH` and `DELETE`, each declared once as an
`Endpoint` the server and any caller share.

```go
// Its own process, in main.go:
app, err := gesso.New(gesso.Config{Env: env, Database: sqlite.Open("notes.db")})
err = app.Register(auth.New(), notes.New())

// From collage, in another process — typed, no code generated:
remote := gesso.NewRemote("http://api.internal:8081", nil)
note, err := notes.GetNote.Call(ctx, remote, models.ReqNoteKey{Key: "jam"}, gesso.Bearer(tok))

// Or inside collage, which passes the full path on:
gessoAPI, err := api.New(cmp.Or(os.Getenv("DATABASE_URL"), "site.db"), "/api") // gesso init wrote it
err = site.Handle("/api/", gessoAPI)
// …the collage-gesso plugin runs its lifecycle, in plugins.go:
gesso.New(gesso.Options{App: gessoAPI}) // collage-gesso, imported as gesso there
// …and pages call it in the same process, with the same code:
note, err := notes.GetNote.Call(ctx, gesso.NewLocal(gessoAPI), models.ReqNoteKey{Key: "jam"}, gesso.Bearer(tok))
```

Mounted in collage, the [collage-gesso](https://github.com/Elagoht/collage-gesso)
plugin starts the app's modules when collage serves and stops them, then runs
the OnShutdown hooks — the database closes there — after collage's server has
stopped.

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso version          # which gesso this is
gesso help             # every command: routes, build, add endpoint, check …
gesso new example.com/events -dialect postgres   # main.go, register.go, a test
cd events && go mod tidy
gesso dev              # run it with .env; rebuilt and restarted as you edit
gesso add module events -field title:string:max=200 -field slug:string:key,slug -field day:date:sort
                       # internal/events from its model, registered in register.go
gesso add module search -empty   # no table, for endpoints that are not CRUD
gesso add endpoint events/stats -method GET -res total:int
gesso add auth         # sign-in: the users module, a model of your own
gesso add guard admin  # internal/guards/admin.go; -guard admin on add module
gesso migrate new add_event_city -dir internal/events/migrations
gesso gen              # models.go and queries.sql → *.gen.go and migrations; gen -check in CI
gesso check            # database calls without ctx, stale generated files
```

[docs/](docs/README.md) is the guide, [DESIGN.md](DESIGN.md) the spec,
[CHANGELOG.md](CHANGELOG.md) what changed.
