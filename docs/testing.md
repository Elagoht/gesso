# Testing

```go
func TestCreateNote(t *testing.T) {
	tx := gessotest.Tx(t, testDB) // rolled back when the test ends
	app := gessotest.New(t, gesso.Config{}, notes.New(notes.Deps{DB: tx}))
	c := gessotest.NewClient(t, app)

	res := c.Post("/notes", gessotest.JSON(models.ReqCreateNote{Title: "Jam"})).ExpectStatus(201)
	got := gessotest.Decode[models.ResNote](res)

	c.Post("/notes", gessotest.JSON(models.ReqCreateNote{})).ExpectProblem(400, "invalid_input")
}
```

Each request in a test transaction runs in a savepoint, so a request that fails
— PostgreSQL then fails the whole transaction — does not spoil the next.

Services are tested without a database: the scaffold's `service_test.go` hands
the service a fake store, and `gessotest.FakeDB` lets one that calls `db.InTx`
run without one.

Every contract has a fake and a suite that holds the real implementation and
the fake to the same behaviour: `storagetest`, `cachetest`, `tokentest`,
`locktest`, `queuetest`. An implementation of your own — S3 storage, a Redis
cache — passes the same suite:

```go
storagetest.Run(t, func(t *testing.T) storage.Storage { return s3store.New(…) })
```

`clock.Manual` moves time in tests; `mail.Capture` keeps what was sent.
