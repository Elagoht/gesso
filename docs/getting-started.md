# Getting started

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso new example.com/notes -dialect sqlite      # or postgres, mysql
cd notes
go mod tidy
gesso add auth                                   # sign-in, a users table
gesso add module notes -guard signedIn -field title:string:max=200 -field body:text
go run . users add ada                           # the password is asked for
gesso dev
```

`gesso dev` builds the program and runs it in development, and rebuilds and
restarts it whenever a `.go`, `.sql`, `go.mod` or environment file changes. A
build that fails leaves the running version up and prints why. Ctrl-C stops
both.

The program's `main` is one line, `gesso.Main(newApp)`, as in collage. It
reads the environment file itself — `.env.development` under `gesso dev` (or
`.env` when there is none), `.env` when built and run — `KEY=value` lines, a
variable your shell sets winning. So a deployed binary needs only a `.env`
beside it.

`gesso new` writes:

| File | What it is |
| --- | --- |
| `main.go` | `gesso.Main(newApp)`; `newApp` makes the app — the database from `DATABASE_URL`, `/openapi.json`, Swagger UI at `/swagger`, the access log — then calls `register` |
| `register.go` | `register(app)`: what `gesso add` adds to — `app.Register(…)` with the modules, `app.Command` and `app.Use` lines |
| `main_test.go` | starts the app on an empty database |

```go
func newApp(env gesso.Env) (*gesso.App, error) {
	app, err := gesso.New(gesso.Config{
		Env:       env,
		Database:  sqlite.Open(cmp.Or(os.Getenv("DATABASE_URL"), "notes.db")),
		OpenAPI:   &gesso.OpenAPIConfig{Title: "notes", Version: "0.1.0", Path: "/openapi.json", UI: "/swagger"},
		AccessLog: true,
	})
	if err != nil {
		return nil, err
	}
	return app, register(app)
}
```

```go
func register(app *gesso.App) error {
	return app.Register(auth.New(), notes.New())
}
```

`Env` is what `Main` read: `-addr` or `ADDR`, `BASE_PATH`, development or
not, the logger. The app opens the database, migrates it as it starts and
closes it when it stops; see [The database](database.md).

In a module that exists — a collage site — `gesso init -dialect sqlite` writes
only `internal/api`; see [With collage](collage.md).

## A module from a model

`gesso add module` writes `internal/<name>/models/models.go`, runs `gesso gen`
on it, and adds `<name>.New()` to `register.go`. Each field is
`name:type[:options and rules]`:

```sh
gesso add module docs -guard signedIn \
  -field title:string:listable,max=200 \
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
| `listable` | the list answers only the listable fields, the id and the key; get answers everything |
| `filterable` | `?<field>=value` keeps the list to the records that match it exactly |
| `orderable` | `?order=<field>` or `?order=-<field>` orders the list |
| `default=<value>` | what a create or a PUT that leaves the field out gets: `draft`, `5`, `true`, `@today` (a date), `@now` (a time), `@user` (the signed-in user's name) |
| `guard=<guard>` | only callers who pass the guard see or write the field |
| `gate=<guard>` | on a `bool`: a record with it false is there only for callers who pass the guard |

Anything else is a validation rule — `max=200`, `email`, `slug`,
`oneof=draft published` — checked when the module is made. A string is
`max=255` unless it says otherwise. `-fields title:string,body:text` is the
short form, without options. `-guard <guard>` guards the writes, `-read
<guard>` the reads, `-sortable` keeps the records in the client's order; see
[Modules and handlers](modules-and-handlers.md). The dialect is the one
`main.go` imports; `-dialect` names another.

The model is the source. `internal/docs/models/models.go` holds it:

```go
//gesso:table docs
//gesso:write signedIn
type Doc struct {
	ID        int64
	Title     string     `gesso:"listable,max=200"`
	Slug      string     `gesso:"key,slug,max=100"`
	Order     int64      `gesso:"optional,sort"`
	Content   string     `gesso:"text"`
	Published *date.Date `gesso:"optional"`
}
```

Change it — a field, an option, a rule, a directive — and run `gesso gen`: it
rewrites the `*.gen.go` files and writes a migration for what changed: a new
column (rows that exist get its zero value), an index, a unique. A field taken
away is a dropped column, and its data: `gen` refuses it without `-drop`. A
type change is yours to migrate (`gesso migrate new`), then `gesso gen
-no-migration`. `models/schema.gen.json` is what `gen` compares with — commit
it.

```
internal/docs/
  module.gen.go       Module, New, Init, Routes, the handlers, the Endpoints
  repository.gen.go   the SQL
  migrations/         embedded; the app applies them as it starts
  models/
    models.go           Doc, //gesso:table — yours
    requests.gen.go     ReqCreateDoc, ReqDocKey, ReqListDocs … — what clients send
    responses.gen.go    ResDoc, ResDocItem — what they get back
    schema.gen.json     what gen compares the model with
```

Never edit a `*.gen.go` file. What the generated code does not do — a rule
before a write, an endpoint of your own — goes in files of your own in the
module's package: hooks and `routes`, see [Modules and
handlers](modules-and-handlers.md).

A module whose endpoints are not create-read-update-delete — a search, a
report — starts from `gesso add module search -empty`: `module.go` with `Init`
and `Routes`, and `models/requests.go`, `models/responses.go`, with no table
and no migration. `gesso add endpoint <module>/<name>` adds an endpoint to
either kind. Sign-in is not scaffolded but wired: `gesso add auth`; guards are
`gesso add guard <name>` and `-guard <name>`, see [Identity and
limits](identity-and-limits.md). Then:

```sh
curl -X POST localhost:8080/auth/login -H 'content-type: application/json' \
  -d '{"username":"ada","password":"…"}'                  # {"token":"gso_…", …}
curl -X POST localhost:8080/notes -H 'authorization: Bearer gso_…' \
  -H 'content-type: application/json' -d '{"title":"Hello","body":"…"}'
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
gesso gen          # models.go and queries.sql → the *.gen.go files
gesso check        # database calls without ctx, stale generated files
```
