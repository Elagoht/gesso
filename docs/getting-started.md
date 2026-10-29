# Getting started

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso new example.com/notes -dialect sqlite      # or postgres, mysql
cd notes
go mod tidy
gesso add module notes -fields title:string,body:text,published_on:date
gesso add auth                                   # sign-in, a users table
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
| `internal/api/api.go` | `New` reads `DATABASE_URL` and `BASE_PATH`; opens the database, migrates it as the app starts, serves `/openapi.json`, adds the `migrate` command |
| `internal/api/register.go` | the lists `gesso add` adds to: modules, migrations, gesso's own tables, commands, constraints |
| `internal/api/api_test.go` | starts the app on an empty database |

In a module that exists — a collage site — `gesso init -dialect sqlite` writes
only `internal/api`; see [With collage](collage.md).

`gesso add module` scaffolds a module — models and endpoints, handlers, a
service, a repository, a fake-store test and its migration — under `internal/`,
and adds it to the `modules` and `migrations` lists in `internal/api/register.go`.
The dialect comes from that file. Field types:

| Type | Go | Rule | Column |
| --- | --- | --- | --- |
| `string` | `string` | `required,max=255` | TEXT (VARCHAR(255) in MySQL) |
| `text` | `string` | `required` | TEXT |
| `int` | `int64` | | BIGINT / INTEGER |
| `bool` | `bool` | | BOOLEAN |
| `float` | `float64` | | DOUBLE |
| `time` | `time.Time` | `required` | TIMESTAMPTZ / TIMESTAMP / DATETIME(6) |
| `date` | `date.Date` | `required` | DATE |

A column named after an SQL keyword (`order`, `group`) is quoted for you. The
scaffolded code is yours: rename, add, delete.

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
