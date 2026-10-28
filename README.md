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
- **An outbox queue**: jobs commit with the data they are about; at-least-once,
  leased, fenced.
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
```

Mounted in collage, nothing starts or shuts the app down: collage has no
shutdown hook for application code. Modules with background work (Starter,
Stopper) and OnShutdown hooks need gesso as its own process, until the
collage-gesso bridge exists.

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso add module events -fields title:string,seats:int,starts_at:time
gesso migrate new add_event_city -dir internal/events/migrations
gesso gen              # queries.sql → typed queries.gen.go; gen -check in CI
gesso check            # database calls without ctx, service methods without ctx
```

`integration/notes` is a complete module; [DESIGN.md](DESIGN.md) is the spec.
