# Identity and limits

## Sign-in: the users module

```sh
gesso add auth -field name:string:optional,self,collapse,max=100 \
               -field avatar:image:self,public,fill=256x256
go run . users add ada -role admin      # the first user; the password is asked for
gesso add module notes -guard signedIn  # writes need a signed-in user
```

`add auth` makes the users a module of the app — `internal/users`, its model
yours to change — and writes the `signedIn` and `admin` guards:

```go
//gesso:table users
//gesso:auth
//gesso:write admin
//gesso:read admin
type User struct {
	ID       int64
	Username string `gesso:"key,lower,max=100"`
	Name     string `gesso:"optional,self,collapse,max=100"`
	Avatar   string `gesso:"image,self,public,fill=256x256"`
}
```

`//gesso:auth` makes the model the users who sign in: its key is `Username`,
and the table keeps a password hash, the roles and when the user was made in
columns of its own, never answered. Sign-in itself is gesso/auth's — its code
is not copied in, so a fix reaches the app with `go get` — and the module is
the app's authenticator.

| Endpoint | |
| --- | --- |
| `POST /auth/login` | `{"username", "password"}` → `{"token", "expiresAt"}` |
| `POST /auth/logout` | revokes the token it is called with |
| `PUT /auth/password` | `{"currentPassword", "newPassword"}`: signs every device out, answers a new token |
| `GET /auth/me` | the signed-in user, every field, and `roles` |
| `PATCH /auth/me` | the fields marked `self`, on oneself: JSON, or a form with the files marked `self` too |
| `PUT`, `DELETE /auth/me/<field>` | a file marked `self`, on oneself |
| `GET`, `POST /users`, `GET`, `PUT`, `PATCH`, `DELETE /users/{username}` | the users, under `//gesso:write` and `//gesso:read` (`admin`) |
| `PUT /users/{username}/password` | `{"password"}`: an administrator sets one |

`POST /users` takes the fields, a `password` and `roles`. Nobody changes their
own roles: `self` is for the fields a user may change, and `roles` is never
one. A field marked `self` has no guard; a user's other fields, and every field
of a user without `self`, are the administrators'. The fields beside the key
take a default, so `users add` from the command line makes a user; for that
reason no field but the key is `unique`.

A sign-in lasts 30 days. Attempts: 5 a minute per address and username, 20 per
address — never per username alone. Behind a proxy, set
`auth.Options.TrustedProxies`. In code, the module's `Users()` is the
`*auth.SQL` on the table — `Create` for a seed or a test:

```go
u, _ := gesso.ModuleOf[*users.Module](app)
_, err := u.Users().Create(ctx, "ada", password, "admin")
```

The `users` command: `add <name> [-role r]...`, `password <name>`, `roles
<name> [role]...`, `delete <name>`, `list`. Each request reads its user again:
a role taken away or a user deleted counts from the next request.
`gesso.IdentityFrom(ctx)` gives the user's id as `Subject`, the username as
`Name`, and the roles.

Without a model of your own, `auth.New()` in `register.go` keeps the users in
`gesso_users` and serves `GET /auth/me` as `{"id", "username", "roles"}`.
Another source of users — one from your own settings (`auth.NewSingle`), a
directory — is an `auth.Users`: `auth.New(auth.Options{Users: users})` in
`register.go`. `auth/authtest` is its contract.

## Guards

```sh
gesso add guard admin                  # internal/guards/admin.go
gesso add module reports -guard admin  # gesso.Guards(guards.Admin) on the writes
```

A new guard refuses everyone until you write its check:

```go
func Admin(r *http.Request) error {
	return gesso.RequireRole("admin")(r)
}
```

`-guard` goes on create, update, patch, delete and reorder (`//gesso:write`),
`-read` on get and list (`//gesso:read`); without `-read`, reads stay open.
Both refuse a guard that does not exist. A guard can also hide a field, or
records, from those who do not pass it: see [Modules and
handlers](modules-and-handlers.md).

## Bearer tokens

The auth module issues them; for a sign-in of your own:

```go
// in your sign-in module's Init
m.tokens = token.NewIssuer(token.NewSQL(app.DB(), app.Dialect()), nil, 30*24*time.Hour)
app.Schema(token.Migrate) // its table

// registered, the module is the app's authenticator
func (m *Module) Authenticate(r *http.Request) (gesso.Identity, bool, error) {
	return m.tokens.Authenticate(r)
}

raw, rec, err := m.tokens.Issue(ctx, userID, "editor") // at sign-in: give raw to the client
err = m.tokens.Revoke(ctx, token.FromRequest(gesso.Request(ctx)))
```

Tokens are opaque (`gso_…`); only their SHA-256 is stored. No credential is
anonymous; a bad one is a 401, never anonymous. gesso reads bearer tokens only,
never a cookie.

## Guards in code

```go
CreateNote.Handle(r, m.create, gesso.Guards(gesso.RequireIdentity))
r.Group("/admin", func(g *gesso.Router) { g.Guard(gesso.RequireRole("admin")) … })
id, ok := gesso.IdentityFrom(ctx)
if gesso.Allowed(ctx, guards.Admin) { … }
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
app.Schema(ratelimit.Migrate) // the gesso_ratelimit table
limit := ratelimit.New(ratelimit.Options{
	Requests: 5, Per: time.Minute,
	Store:    ratelimit.NewSQL(app.DB(), app.Dialect()),
})
```

Run `store.Purge(ctx, time.Now())` now and then — from a scheduled job — to
delete clients whose bucket is full again. If the database fails, requests are
let through and the failure logged.

## Idempotency keys

A client that retries a POST — the network failed after it was sent — must
not pay twice. With the `idempotency` middleware it sends `Idempotency-Key`:

```go
app.Schema(idempotency.Migrate)
keys := idempotency.New(idempotency.Options{Store: idempotency.NewSQL(app.DB(), app.Dialect())})
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
}})
```

gesso answers the browser's preflight (`OPTIONS`) for a route that exists, and
marks the answers to the origins listed. `"*"` allows any origin, but not with
`Credentials` (cookies), which would let any site act as the signed-in user. A
collage site serving its pages and the API from one origin needs no CORS.
