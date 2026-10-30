<!-- gesso:begin @@VERSION@@ — written by gesso ai init; run it again after upgrading gesso -->
## gesso

This is a JSON API on gesso (github.com/Elagoht/gesso). Use the CLI, don't hand-write what it makes:

| To add | Run |
| --- | --- |
| a CRUD module | `gesso add module <name> -field <field>:<type>[:options,rules] …` (`-guard`, `-read`, `-sortable`) |
| a module with no table | `gesso add module <name> -empty` |
| one endpoint in a module | `gesso add endpoint <module>/<name> -method PUT -field …` |
| sign-in | `gesso add auth [-field …]` (the users module `internal/users`, a `//gesso:auth` model; `app users add <name>`; the `signedIn` and `admin` guards) |
| a guard | `gesso add guard <name>`, then `-guard <name>` on add module / add endpoint |
| a command / middleware | `gesso add command <name>` / `gesso add middleware <name>` |
| a migration of your own | `gesso migrate new <name>` |

- **A module's model is its source.** `internal/<module>/models/models.go` holds a `//gesso:table` struct (`//gesso:write`, `//gesso:read`, `//gesso:sortable`, `//gesso:auth` beside it) with `gesso:"…"` tags (`key`, `unique`, `index`, `optional`, `sort`, `text`, `listable`, `filterable`, `orderable`, `default=<value>` (`@today`, `@now`, `@user` or a literal), `guard=<guard>`, `gate=<guard>`, `trim`/`lower`/`upper`/`collapse`/`slugify`, `file`/`image` with `accept=`, `maxsize=`, `public`, `routes`, `fit=`, `fill=`, `format=`, `quality=`, and `self` on the users model, validate rules). Files come with the record in a `multipart/form-data` create, PUT or PATCH (sent empty, a file goes), never in a JSON body; `GET /<module>/{key}/files/{field}` serves them; `routes` adds `PUT`/`DELETE /<module>/{key}/<field>`. Change it, then run `gesso gen`: it rewrites the `*.gen.go` files and writes the migration. Never edit a `*.gen.go` file or `schema.gen.json`.
- Code of your own in a generated module goes in files of its package: `BeforeCreate`, `BeforeUpdate`, `BeforeDelete` and `routes(r)` on `*Module`. Run `gesso gen` after adding one.
- `gesso gen` refuses a dropped field without `-drop` and any type change; for a type change write the migration (`gesso migrate new`) and run `gesso gen -no-migration`.
- Custom SQL goes in the module's `queries.sql`; `gesso gen` turns it into `queries.gen.go`.
- What clients send is `Req…` (`models/requests.go`), what they get back `Res…` (`models/responses.go`).
- `main.go` makes the app (`newApp`); `register.go` registers modules, commands and middleware; the CLI adds to it. A module takes the database and the rest in `Init(app *gesso.App)`: `app.DB()`, never a global.
- Every database call takes `ctx` first.
- Settings come from the environment or `.env` (`.env.development` under `gesso dev`); read them in `newApp`, nowhere else.
- Before finishing: `go vet ./...`, `go test ./...`, `gesso check`.
<!-- gesso:end -->
