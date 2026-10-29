# Identity and limits

## Sign-in: the auth module

```sh
gesso add auth                # one user: AUTH_USERNAME and AUTH_PASSWORD
gesso add auth -users sql     # a table of users, with argon2id passwords
```

wires the `auth` package into `internal/api/api.go`. No code of it is copied
into the app: a fix to sign-in reaches it with `go get`. What it adds:

```go
authn, err := auth.New(auth.Options{DB: d, Dialect: dialect, Users: auth.NewSingle(cfg.AuthUsername, cfg.AuthPassword)})
app, err := gesso.New(gesso.Config{Authenticator: authn, …}, schema{…}, authn, …)
// and token.Migrate — auth.Migrate for -users sql — in stores()
```

| Endpoint | |
| --- | --- |
| `POST /auth/login` | `{"username", "password"}` → `{"token", "expiresAt"}` |
| `POST /auth/logout` | revokes the token it is called with |
| `GET /auth/me` | `{"id", "username", "roles"}` |
| `PUT /auth/password` | `{"currentPassword", "newPassword"}`: signs every device out, and answers a new token; with `-users sql` |

They are `auth.Login`, `auth.Logout`, `auth.Me` and `auth.ChangePassword`, so
a collage page calls them with `gesso.NewLocal`. A sign-in lasts 30 days
(`TokenTTL`). Attempts are limited to 5 a minute per client address and
username, and 20 per address — never per username alone, which would let anyone
lock the user out; behind a proxy, give `Options.TrustedProxies`, or every
client shares the proxy's address.

Who signs in is an `auth.Users`:

- `auth.NewSingle(username, password, roles...)` — a personal site, an admin
  of one. Its id is the username. The app refuses to start without both.
- `auth.NewSQL(pool, dialect)` — the `gesso_users` table: usernames matched
  exactly, argon2id hashes (made again with stronger parameters at the next
  sign-in), roles. There is no sign-up endpoint; `SQL.Create` is there for one
  of your own. The app gets a `users` command:

  ```sh
  app users add ada -role admin    # asks for the password, twice, unechoed
  app users password ada           # an operator's reset: signs them out
  app users roles ada editor       # replaces the roles: signs them out
  app users delete ada
  app users list
  ```

- your own — a directory, another service: `Authenticate` and `Lookup`, and
  `ChangePassword` for `PUT /auth/password`. `auth/authtest` holds it to the
  contract NewSingle and NewSQL keep.

## Bearer tokens

The auth module issues them; for a sign-in of your own:

```go
tokens := token.NewIssuer(token.NewSQL(pool, migrate.Postgres), nil, 30*24*time.Hour)
// token.Migrate(ctx, pool, migrate.Postgres) creates its table

app, err := gesso.New(gesso.Config{Authenticator: tokens}, login.New(tokens), notes.New(…))

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

## Idempotency keys

A client that retries a POST — the network failed after it was sent — must
not pay twice. With the `idempotency` middleware it sends `Idempotency-Key`:

```go
idempotency.Migrate(ctx, pool, migrate.Postgres)
keys := idempotency.New(idempotency.Options{Store: idempotency.NewSQL(pool, migrate.Postgres)})
r.Group("/orders", func(g *gesso.Router) { g.Use(keys.Middleware()); … })
```

The first request with a key runs and its answer is kept (24h); a retry gets
it again, `Idempotent-Replayed: true`. A retry while the first runs is 409,
the key with another body 422; a 5xx frees the key.

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
