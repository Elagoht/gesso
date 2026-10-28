# gesso

A Go backend framework for JSON APIs, made to sit under
[collage](https://github.com/Elagoht/collage) — or anything else — and to need
nothing from it. The standard library and `golang.org/x/crypto`, nothing else.

- **Modules** with explicit dependencies: no singletons, no container.
- **Typed handlers**: `func(ctx, In) (Out, error)`; input decoded from path, query,
  header and body, validated before your code runs.
- **Guards and bearer tokens**: opaque, revocable, only their hash stored.
- **OpenAPI 3.1** generated from the handlers' types and rules — it cannot drift.
- **problem+json** for every error, the router's 404 and 405 included.
- **Transactions that nest**: `db.InTx` over `*sql.DB`, or as a savepoint inside a
  test's transaction.
- **Migrations** per module, timestamped, checksummed, locked.
- **PostgreSQL, MySQL and SQLite**, each verified against a real server in CI;
  no ORM, and no driver chosen for you.
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
type GetIn struct {
    Slug string `path:"slug"`
}

// Declared once; the server and any caller share it.
var GetNote = gesso.Endpoint[GetIn, Note]{Method: "GET", Pattern: "/notes/{slug}"}

func (m *Module) Routes(r *gesso.Router) {
    GetNote.Handle(r, m.get)
}

func (m *Module) get(ctx context.Context, in GetIn) (Note, error) {
    return m.svc.Get(ctx, in.Slug) // sql.ErrNoRows answers 404
}
```

```go
// Its own process:
app, err := gesso.New(gesso.Config{Addr: ":8081"}, notes.New(notes.Deps{DB: pool}))
app.OnShutdown(func(context.Context) error { return pool.Close() })
err = app.Run()

// From collage, in another process — typed, no code generated:
api := gesso.NewRemote("http://api.internal:8081", nil)
note, err := notes.GetNote.Call(ctx, api, notes.GetIn{Slug: "jam"}, gesso.Bearer(tok))

// Or inside collage, which passes the full path on:
api, err := gesso.New(gesso.Config{BasePath: "/api"}, notes.New(notes.Deps{DB: pool}))
err = site.Handle("/api/", api)
// …the collage-gesso plugin runs its lifecycle, in plugins.go:
gesso.New(gesso.Options{App: api}) // collage-gesso, imported as gesso there
// …and pages call it in the same process, with the same code:
note, err := notes.GetNote.Call(ctx, gesso.NewLocal(api), notes.GetIn{Slug: "jam"}, gesso.Bearer(tok))
```

Mounted in collage, the [collage-gesso](https://github.com/Elagoht/collage-gesso)
plugin starts the app's modules when collage serves and stops them, then runs
the OnShutdown hooks, after collage's server has stopped.

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso new example.com/events -dialect postgres   # main, app wiring, a test
cd events && go mod tidy
gesso add module events -fields title:string,seats:int,starts_at:time,day:date,body:text
                       # scaffolded under internal/, and wired into internal/api
gesso migrate new add_event_city -dir internal/events/migrations
gesso gen              # queries.sql → typed queries.gen.go; gen -check in CI
gesso check            # database calls without ctx, service methods without ctx
```

`integration/notes` is a complete module; [docs/](docs/README.md) is the guide,
[DESIGN.md](DESIGN.md) the spec, [CHANGELOG.md](CHANGELOG.md) what changed.
