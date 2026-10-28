# gesso

A Go backend framework for JSON APIs, made to sit under
[collage](https://github.com/Elagoht/collage) — or anything else — and to need
nothing from it. Standard library only.

- **Modules** with explicit dependencies: no singletons, no container.
- **Typed handlers**: `func(ctx, In) (Out, error)`; input decoded from path, query,
  header and body, validated before your code runs.
- **problem+json** for every error, the router's 404 and 405 included.
- **Transactions that nest**: `db.InTx` over `*sql.DB`, or as a savepoint inside a
  test's transaction.
- **Migrations** per module, timestamped, checksummed, locked.
- **gessotest**: in-process client, problem assertions, rolled-back test
  transactions.

```go
type GetIn struct {
    Slug string `path:"slug"`
}

func (m *Module) Routes(r *gesso.Router) {
    r.Get("/notes/{slug}", gesso.Handle(m.get))
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

// Or inside collage, which passes the full path on:
api, err := gesso.New(gesso.Config{BasePath: "/api"}, notes.New(notes.Deps{DB: pool}))
err = site.Handle("/api/", api)
```

Mounted in collage, nothing starts or shuts the app down: collage has no
shutdown hook for application code. Modules with background work (Starter,
Stopper) and OnShutdown hooks need gesso as its own process, until the
collage-gesso bridge exists.

`integration/notes` is a complete module; [DESIGN.md](DESIGN.md) is the spec.
