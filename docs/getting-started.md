# Getting started

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso new example.com/notes -dialect sqlite      # or postgres, mysql
cd notes
go mod tidy
gesso add module notes -fields title:string,body:text,published_on:date
go run .
```

`gesso new` writes:

| File | What it is |
| --- | --- |
| `main.go` | reads `ADDR`, `BASE_PATH` and `DATABASE_URL`, builds the app, runs it until SIGINT or SIGTERM |
| `internal/app/app.go` | opens the database, runs every module's migrations, maps database errors to answers, serves `/openapi.json` |
| `internal/app/app_test.go` | starts the app on an empty database |

`gesso add module` scaffolds a module — models and endpoints, handlers, a
service, a repository, a fake-store test and its migration — under `internal/`,
and adds it to the `modules` and `migrations` lists in `internal/app/app.go`.
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
scaffolded code is yours: rename, add, delete. Then:

```sh
curl -X POST localhost:8080/notes -H 'content-type: application/json' \
  -d '{"title":"Hello","body":"…","publishedOn":"2026-10-28"}'
curl localhost:8080/notes
curl localhost:8080/openapi.json
```

Other commands:

```sh
gesso migrate new add_notes_slug -dir internal/notes/migrations
gesso gen          # queries.sql → typed queries.gen.go
gesso check        # database calls and service methods without ctx
```
