# With collage

[collage](https://github.com/Elagoht/collage) renders pages and never does the
backend; gesso is the backend. They run as two processes or as one.

## Two processes

The API runs on its own (`gesso new`'s `main.go`), and collage calls it with the
endpoints both import:

```go
api := gesso.NewRemote("http://api.internal:8080", nil)
note, err := notes.GetNote.Call(ctx, api, notes.GetIn{Slug: slug}, gesso.Bearer(token))
```

The token comes from the API's sign-in and lives in collage's session.

## One process

collage mounts the app with its own `Handle`, which keeps middleware off an API,
and the [collage-gesso](https://github.com/Elagoht/collage-gesso) plugin runs
the app's lifecycle: its modules start when collage serves and stop after
collage's server has stopped. Pages call it with `gesso.NewLocal(api)` instead
of `NewRemote`; nothing else changes.

```go
api, err := gesso.New(gesso.Config{BasePath: "/api"}, notes.New(…))
app, err := collage.New(&collage.Config{Plugins: plugins(api)})
err = app.Handle("/api/", api)
```

The plugin's README shows the `plugins.go` that `collage plugin add` leaves.
