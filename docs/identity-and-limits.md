# Identity and limits

## Bearer tokens

```go
tokens := token.NewIssuer(token.NewSQL(pool, migrate.Postgres), nil, 30*24*time.Hour)
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

A request a collage page makes in process (`gesso.NewLocal`) comes from the
address the page passes with `gesso.ClientAddr` — the end client's, which
collage reads with `collage.ClientIP(r)`; without it every such call comes from
127.0.0.1 and all of them share one bucket. Over the network, `ClientAddr`
sends X-Forwarded-For: list the caller (collage's address) in
`TrustedProxies` for the API to read it. An IPv6 client counts by its /64.

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

## CORS

A browser page on another origin — a single-page app on `app.example.com`
calling `api.example.com` — needs the API's leave:

```go
gesso.New(gesso.Config{CORS: &gesso.CORS{
	Origins: []string{"https://app.example.com"},
}}, …)
```

gesso answers the browser's preflight (`OPTIONS`) for a route that exists, and
marks the answers to the origins listed. `"*"` allows any origin, but not with
`Credentials` (cookies), which would let any site act as the signed-in user. A
collage site serving its pages and the API from one origin needs no CORS.
