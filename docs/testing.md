# Testing

```go
func TestCreateNote(t *testing.T) {
	app := gessotest.New(t, gesso.Config{
		Database:      sqlite.Open(filepath.Join(t.TempDir(), "test.db")),
		Authenticator: gessotest.Identities{"ada": {Subject: "1", Name: "ada"}},
	}, notes.New())
	c := gessotest.NewClient(t, app).WithHeader("Authorization", "Bearer ada")

	res := c.Post("/notes", gessotest.JSON(models.ReqCreateNote{Title: "Jam", Body: "…"})).ExpectStatus(201)
	got := gessotest.Decode[models.ResNote](res)

	c.Post("/notes", gessotest.JSON(models.ReqCreateNote{})).ExpectProblem(400, "invalid_input")
}
```

`gessotest.New` builds the app, starts it — the database migrated — and shuts
it down when the test ends. `gesso new`'s `main_test.go` starts the whole app
the same way, through `newApp`.

A repository of your own over a `db.DB` runs in `gessotest.Tx(t, d)`, a
transaction rolled back when the test ends. Each request a test's client sends
then runs in a savepoint, so a request that fails — PostgreSQL then fails the
whole transaction — does not spoil the next. `gessotest.FakeDB` lets code that
calls `db.InTx` run without a database.

Every contract has a fake and a suite that holds the real implementation and
the fake to the same behaviour: `storagetest`, `cachetest`, `tokentest`,
`locktest`, `queuetest`. An implementation of your own — S3 storage, a Redis
cache — passes the same suite:

```go
storagetest.Run(t, func(t *testing.T) storage.Storage { return s3store.New(…) })
```

`clock.Manual` moves time in tests; `mail.Capture` keeps what was sent.

## Signed in

```go
_, err := notes.CreateNote.Call(ctx, gesso.NewLocal(app), in, gesso.Bearer("ada"))
```

`gessotest.Identities` takes a bearer token for the identity it names; no token
is anonymous. Against the auth module itself, register `auth.New()` and make
the user:

```go
app := gessotest.New(t, gesso.Config{Database: sqlite.Open(filepath.Join(t.TempDir(), "test.db"))},
	auth.New(), notes.New())
authn, _ := gesso.ModuleOf[*auth.Module](app)
_, err := authn.Users().(*auth.SQL).Create(ctx, "ada", password)
bearer := gessotest.SignIn(t, gesso.NewLocal(app), "ada", password)
```

`gessotest.SignIn` signs in through `auth.Login` and is the bearer to call
with.
