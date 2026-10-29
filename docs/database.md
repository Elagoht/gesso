# The database

`Config.Database` is the app's database, one of:

```go
sqlite.Open("notes.db")                       // gesso/sqlite: a file
postgres.Open("postgres://…@host:5432/notes") // gesso/postgres
mysql.Open("user:pass@tcp(host:3306)/notes")  // gesso/mysql
```

The app opens it, migrates it as it starts, closes it at shutdown, answers its
constraint violations as field errors, adds the `migrate` command, and, when
`Config.Health` is set, a `database` readiness check. Modules take the pool
in `Init` with `app.DB()`.

gesso has no ORM. Repositories take a `db.DB` — what `*sql.DB` and `*sql.Tx`
both are — so the same code runs on the pool or in a transaction:

```go
err := db.InTx(ctx, m.app.DB(), func(tx db.DB) error {
	if err := m.store(tx).Insert(ctx, note); err != nil {
		return err
	}
	_, err := SendWelcome.Enqueue(ctx, m.queue, tx, Welcome{…}) // commits with it
	return err
})
```

An `InTx` inside a transaction is a savepoint.

## Migrations

Each module carries `migrations/` (`20261028153000_create_notes.up.sql`, with an
optional `.down.sql`), embedded and added in `Init` with `app.Migrations`; the
app applies them all as one history ordered by version. Applied files are
checksummed: an edited one stops `Up`, as does an older one that arrives after
a newer one ran. A file whose first line is `-- gesso:no-transaction` runs
outside a transaction. A library's tables — the queue's, the rate limits' — are
a step of their own after the files: `app.Schema(queue.Migrate)`.

## Dialects

| | PostgreSQL | MySQL | SQLite |
| --- | --- | --- | --- |
| package | `gesso/postgres`, pgx | `gesso/mysql`, go-sql-driver | `gesso/sqlite`, modernc (no cgo) |
| placeholders | `$1` | `?` | `?` |
| migration lock | advisory lock | `GET_LOCK` | the write lock |
| DDL in a transaction | yes | **no**: keep one DDL statement per migration | yes |
| driver settings | — | `parseTime`; several statements per Exec only for migrations | foreign keys on, WAL, a 5s busy timeout |

A unique constraint's violation is a 409 on the field `app.Constraints` names
for it — `{"docs.slug": "slug"}`; a generated module names its own.

## gesso gen

A module's model (`models/models.go`, `//gesso:table`) is generated from: see
[Getting started](getting-started.md). For queries of your own:

```sql
-- gesso: dialect postgres

-- name: NoteBySlug :one
-- in: slug string
-- out: Note
SELECT id, slug, title FROM notes WHERE slug = $1;
```

`gesso gen` turns a package's `queries.sql` into `queries.gen.go`: `:one`,
`:many`, `:exec`, `:execrows`, and `:execlastid` (MySQL's id of an insert, which
it cannot RETURN). `gesso gen -check` fails when a file is stale.

## Dates

`date.Date` is a calendar day: `"2026-10-28"` in JSON and parameters, DATE in the
database, `format: date` in OpenAPI. Its zero value is `null` / NULL.
