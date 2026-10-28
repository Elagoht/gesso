# Identity and limits

## Bearer tokens

```go
tokens := token.NewIssuer(token.NewSQLStore(pool, migrate.Postgres), nil, 30*24*time.Hour)
// token.Migrate(ctx, pool, migrate.Postgres) creates its table

app, err := gesso.New(gesso.Config{Authenticator: tokens}, auth.New(tokens), notes.New(…))

raw, rec, err := tokens.Issue(ctx, userID, "editor") // at sign-in: give raw to the client
err = tokens.Revoke(ctx, token.FromRequest(gesso.Request(ctx)))
```

Tokens are opaque (`gso_…`); only their SHA-256 is stored. No credential is
anonymous; a bad one is a 401, never anonymous. gesso reads bearer tokens only,
never a cookie.

## Guards

```go
CreateNote.Handle(r, m.create, gesso.Guards(gesso.RequireIdentity))
r.Group("/admin", func(g *gesso.Router) { g.Guard(gesso.RequireRole("admin")) … })
id, ok := gesso.IdentityFrom(ctx)
```

A guard runs before the input is read: a refused caller learns nothing of what
the input should be.

## Rate limits

```go
limit := ratelimit.New(ratelimit.Options{Requests: 5, Per: time.Minute})
r.Group("/auth", func(g *gesso.Router) {
	g.Use(limit.Middleware())
	Login.Handle(g, m.login)
})
```

Each client — its address, or behind `TrustedProxies` the address the nearest
proxy saw — has a token bucket; over it, 429 `too_many_requests` with
`Retry-After`.

By default the buckets are in the process's memory, so N replicas let a client
through N times as often. To share them:

```go
ratelimit.Migrate(ctx, pool, migrate.Postgres) // the gesso_ratelimit table
limit := ratelimit.New(ratelimit.Options{
	Requests: 5, Per: time.Minute,
	Store:    ratelimit.NewSQL(pool, migrate.Postgres), // MySQL, SQLite too
})
```

Run `store.Purge(ctx, time.Now())` now and then — from a scheduled job — to
delete clients whose bucket is full again. If the database fails, requests are
let through and the failure logged.
