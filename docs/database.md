# The database

gesso has no ORM and chooses no driver. Repositories take a `db.DB` — what
`*sql.DB` and `*sql.Tx` both are — so the same code runs on the pool or in a
transaction:

```go
err := db.InTx(ctx, s.db, func(tx db.DB) error {
	if err := s.store(tx).Insert(ctx, note); err != nil {
		return err
	}
	return SendWelcome.Enqueue(ctx, s.queue, tx, Welcome{…}) // commits with it
})
```

An `InTx` inside a transaction is a savepoint.

## Migrations

Each module carries `migrations/` (`20261028153000_create_notes.up.sql`, with an
optional `.down.sql`), embedded and applied as one history ordered by version.
Applied files are checksummed: an edited one stops `Up`, as does an older one
that arrives after a newer one ran. A file whose first line is
`-- gesso:no-transaction` runs outside a transaction.

## Dialects

| | PostgreSQL | MySQL | SQLite |
| --- | --- | --- | --- |
| placeholders | `$1` | `?` | `?` |
| migration lock | advisory lock | `GET_LOCK` | the write lock |
| DDL in a transaction | yes | **no**: keep one DDL statement per migration | yes |
| driver settings | — | `multiStatements=true`, `parseTime=true` | foreign keys on |

`gesso new` writes those settings. Violations become answers through
`db.Errors(classify, fields)`, where `classify` is the driver's three lines
(`gesso new` writes them) and `fields` names the field a constraint guards.

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
