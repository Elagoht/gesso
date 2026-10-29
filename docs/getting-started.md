# Getting started

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso new example.com/notes -dialect sqlite      # or postgres, mysql
cd notes
go mod tidy
gesso add auth                                   # sign-in, a users table
gesso add module notes -guard signedIn -field title:string:max=200 -field body:text
gesso dev
```

`gesso dev` builds the program and runs it in development, and rebuilds and
restarts it whenever a `.go`, `.sql`, `go.mod` or environment file changes. A
build that fails leaves the running version up and prints why. Ctrl-C stops
both.

The program's `main` is one line, `gesso.Main(api.New)`, as in collage. It
reads the environment file itself — `.env.development` under `gesso dev` (or
`.env` when there is none), `.env` when built and run — `KEY=value` lines, a
variable your shell sets winning. So a deployed binary needs only a `.env`
beside it.

`gesso new` writes:

| File | What it is |
| --- | --- |
| `main.go` | `gesso.Main(api.New)`: the environment file, then serve until SIGINT or SIGTERM, or run a command |
| `internal/api/api.go` | `New` reads `DATABASE_URL` and `BASE_PATH`; opens the database, migrates it as the app starts, serves `/openapi.json` and Swagger UI at `/swagger`, adds the `migrate` command |
| `internal/api/register.go` | the lists `gesso add` adds to: modules, migrations, gesso's own tables, commands, constraints |
| `internal/api/api_test.go` | starts the app on an empty database |

In a module that exists — a collage site — `gesso init -dialect sqlite` writes
only `internal/api`; see [With collage](collage.md).

`gesso add module` scaffolds a module under `internal/` and adds it to
`internal/api/register.go`. Each field is `name:type[:options and rules]`:

```sh
gesso add module docs -guard signedIn \
  -field title:string:max=200 \
  -field slug:string:key,slug,max=100 \
  -field order:int:optional,sort \
  -field content:text \
  -field published:date:optional
```

| Type | Go | Column |
| --- | --- | --- |
| `string` | `string` | TEXT (VARCHAR(max) in MySQL) |
| `text` | `string` | TEXT |
| `int` | `int64` | BIGINT / INTEGER |
| `bool` | `bool` | BOOLEAN |
| `float` | `float64` | DOUBLE |
| `time` | `time.Time` | TIMESTAMPTZ / TIMESTAMP / DATETIME(6) |
| `date` | `date.Date` | DATE |

| Option | |
| --- | --- |
| `key` | the URL names a record by it — `/docs/{slug}` — instead of the id; unique |
| `unique` | a unique index; a duplicate is a 409 on the field |
| `index` | an index |
| `optional` | not required; an optional `time` or `date` is a pointer, NULL when left out |
| `sort` | the list is in its order, then the id's |

Anything else is a validation rule — `max=200`, `email`, `slug`,
`oneof=draft published` — checked when the module is made. A string is
`max=255` unless it says otherwise. `-fields title:string,body:text` is the
short form, without options.

The model is the source. `internal/docs/models/models.go` holds it:

```go
//gesso:table docs
type Doc struct {
	ID      int64
	Title   string `gesso:"max=200"`
	Slug    string `gesso:"key,slug,max=100"`
	Order   int64  `gesso:"optional,sort"`
	Content string `gesso:"text"`
}
```

Change it — a field, an option, a rule — and run `gesso gen`: it rewrites the
`*.gen.go` files (requests, responses, the repository) and writes a migration
for what changed: a new column (rows that exist get its zero value), an index,
a unique. A field taken away is a dropped column, and its data: `gen` refuses
it without `-drop`. A type change is yours to migrate (`gesso migrate new`),
then `gesso gen -no-migration`. `models/schema.gen.json` is what `gen` compares
with — commit it.

`module.go`, `endpoints.go`, `handler.go`, `service.go` and its test are
yours: `gen` never touches them, and what they call keeps its shape when
fields change.

A module whose endpoints are not create-read-update-delete — a search, a
report — starts from `gesso add module search -empty`: `module.go`,
`endpoints.go` and `models/requests.go`, `models/responses.go`, with no table
and no migration. Sign-in is not scaffolded but wired: `gesso add auth`;
guards are `gesso add guard <name>` and `-guard <name>`, see
[Identity and limits](identity-and-limits.md). Then:

```sh
curl -X POST localhost:8080/notes -H 'content-type: application/json' \
  -d '{"title":"Hello","body":"…","publishedOn":"2026-10-28"}'
curl localhost:8080/notes
curl localhost:8080/openapi.json
```

and open <http://localhost:8080/swagger> to try the endpoints in a browser.

The app migrates the database as it starts; to look or step back:

```sh
go run . migrate status
go run . migrate down        # the newest; down 3 for three
```

`-addr` (or `ADDR`) says where it listens, `:8080` by default.

Other commands:

```sh
gesso migrate new add_notes_slug -dir internal/notes/migrations
gesso gen          # queries.sql → typed queries.gen.go
gesso check        # database calls and service methods without ctx
```
