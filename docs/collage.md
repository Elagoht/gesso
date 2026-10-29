# With collage

[collage](https://github.com/Elagoht/collage) renders pages and never does the
backend; gesso is the backend. They run as two processes or as one.

## Two processes

The API runs on its own (`gesso new`'s `main.go`), and collage calls it with the
endpoints both import:

```go
api := gesso.NewRemote("http://api.internal:8080", nil)
note, err := notes.GetNote.Call(ctx, api, models.ReqGetNote{Slug: slug}, gesso.Bearer(token))
```

The token comes from the API's sign-in and lives in collage's session.

## One process

collage.Main already reads the site's `.env`, so `os.Getenv` works in
`newApp`. In the collage site's root:

```sh
gesso init -dialect sqlite              # internal/api: database, migrations, OpenAPI
gesso add module notes -fields title:string,body:text   # wired into internal/api
collage plugin add elagoht/gesso        # runs the API's lifecycle
```

Then, in `main.go`'s `newApp`, build the API before `collage.New`, hand it to
`plugins`, and mount it — with the site's own `Handle`, which keeps middleware
off an API:

```go
gessoAPI, err := api.NewWith(api.Config{
	BasePath: "/api",
	Database: cmp.Or(os.Getenv("DATABASE_URL"), "site.db"),
	Logger:   slog.Default(),
})
if err != nil {
	return nil, err
}
// …
app, err := collage.New(&collage.Config{ /* … */ Plugins: plugins(gessoAPI)})
// …
if err := app.Handle("/api/", gessoAPI); err != nil {
	return nil, err
}
if err := register(app, gessoAPI); err != nil {
	return nil, err
}
```

```go
// plugins.go
import gessoapp "github.com/Elagoht/gesso" // the plugin is imported as gesso

func plugins(api *gessoapp.App) []collage.Plugin {
	return []collage.Plugin{gesso.New(gesso.Options{App: api})}
}
```

A page reads the API through `gesso.NewLocal`, which runs the call through the
API's whole pipeline without the network. Pass the end client's address with
`gesso.ClientAddr(collage.ClientIP(r))`, so the API's rate limits count the
visitor, not the site:

```go
// routes.go: pages.Notes(gesso.NewLocal(api))
collage.Load(func(ctx context.Context, _ *collage.RenderContext) (notesView, error) {
	list, err := notes.ListNotes.Call(ctx, api, models.ReqListNotes{Take: 50})
	return notesView{Notes: list}, err
})
```

The modules start when collage serves and stop after collage's server has
stopped; the database closes last. Moving the API to a process of its own later
changes `NewLocal` to `NewRemote` and nothing else.
