# Work outside the request

## The queue

An outbox: a job is written through the caller's transaction, so it exists if
and only if the data it is about was committed.

```go
var SendWelcome = queue.Kind[Welcome]{Name: "send_welcome", MaxAttempts: 5}

// in the service, inside the transaction that creates the user:
_, err := SendWelcome.Enqueue(ctx, q, tx, Welcome{UserID: id})

// wiring: the worker is a module, started and stopped with the app
q := queue.NewPostgres(pool, nil) // NewMySQL, NewSQLite
w := queue.NewWorker(q, queue.WorkerOptions{})
queue.Handle(w, SendWelcome, mailer.SendWelcome)
app, err := gesso.New(cfg, users.New(…), w)
```

`queue.Migrate(ctx, pool, migrate.Postgres)` creates its table. Delivery is at
least once: a handler must be idempotent. A failed job is retried with backoff,
then dead; `q.Dead(ctx, kind, n)` lists dead jobs.

## Schedules

```go
queue.Every(w, "cleanup", Cleanup, time.Hour, CleanupArgs{})       // on the hour
queue.Cron(w, "nightly", Report, "0 3 * * *", ReportArgs{})          // 03:00 in WorkerOptions.Location
```

A schedule enqueues its job once per tick however many replicas run the
worker: the tick is recorded with the job, in one transaction. Ticks missed
while no worker ran fire once, for the latest.

## Events

One module tells others something happened, without knowing who listens:

```go
var NoteDeleted = events.Topic[NoteDeletedEvent]{Name: "notes.deleted"}

NoteDeleted.Subscribe(bus, search.onNoteDeleted)          // in search's wiring
err := NoteDeleted.Publish(ctx, bus, NoteDeletedEvent{Slug: slug}) // in notes
```

`events.NewBus()` is passed to the modules that use it. Subscribers run in
order inside `Publish`, and their errors come back to the publisher. Slow or
must-not-be-lost work: the subscriber enqueues a queue job.

## Locks

```go
l := lock.NewPostgres(pool) // lock.NewMySQL; lock.NewMemory in one process
unlock, ok, err := l.TryLock(ctx, "nightly-report")
if ok {
	defer unlock(ctx)
	…
}
```

A lock is held on a connection of its own and ends with it, so a crashed holder
never leaves one behind.
