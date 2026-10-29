<!-- gesso:begin @@VERSION@@ — written by gesso ai init; run it again after upgrading gesso -->
## gesso

This is a JSON API on gesso (github.com/Elagoht/gesso). Use the CLI, don't hand-write what it makes:

| To add | Run |
| --- | --- |
| a CRUD module | `gesso add module <name> -field title:string:max=200 -field slug:string:key,slug …` |
| one endpoint in a module | `gesso add endpoint <module>/<name> -method PUT -field …` |
| sign-in | `gesso add auth` (users table, `app users add <name>`, the `signedIn` guard) |
| a guard | `gesso add guard <name>`, then `-guard <name>` on add module / add endpoint |
| a command / middleware | `gesso add command <name>` / `gesso add middleware <name>` |
| a migration of your own | `gesso migrate new <name>` |

- **A module's model is its source.** `internal/<module>/models/models.go` holds a `//gesso:table` struct with `gesso:"…"` tags (`key`, `unique`, `index`, `optional`, `sort`, `text`, validate rules). Change it, then run `gesso gen`: it rewrites the `*.gen.go` files and writes the migration. Never edit a `*.gen.go` file or `schema.gen.json`.
- `gesso gen` refuses a dropped field without `-drop` and any type change; for a type change write the migration (`gesso migrate new`) and run `gesso gen -no-migration`.
- Custom SQL goes in the module's `queries.sql`; `gesso gen` turns it into `queries.gen.go`.
- What clients send is `Req…` (`models/requests.go`), what they get back `Res…` (`models/responses.go`).
- `internal/api/register.go` lists the modules, migrations, stores, middleware, commands and constraints; the CLI adds to it. `internal/api/api.go` is setup.
- Every database call and every service or repository method takes `ctx` first. No singletons: dependencies come in through `Deps`.
- Settings come from the environment or `.env` (`.env.development` under `gesso dev`); read them in `api.New`, nowhere else.
- Before finishing: `go vet ./...`, `go test ./...`, `gesso check`.
<!-- gesso:end -->
