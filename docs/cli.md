# The command line

```sh
go install github.com/Elagoht/gesso/cmd/gesso@latest
gesso help                # every command; gesso help <command> for one
```

| Command | |
| --- | --- |
| `gesso new <module path>` | a project: `main.go`, `internal/api`, `.env.example`, a test. `-template demo` adds sign-in and a notes module; `-ai` writes `AGENTS.md`; `-force` writes into a folder that is not empty |
| `gesso init` | `internal/api` in a Go module that exists — a collage site |
| `gesso dev` | run it, rebuilt and restarted as files change, with `.env.development` or `.env` |
| `gesso build` | the binary to deploy: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`, `-os linux -arch amd64` for another machine, `-i` to write a Dockerfile and a systemd unit beside it |
| `gesso routes` | every endpoint: method, path, module, guards, its Req and Res; then the app's commands |
| `gesso inspect` | the same, and each Req's and Res's fields, as JSON — for an editor |
| `gesso check` | `ctx` rules, stale generated files (errors), writes with no guard (warnings); `-json` for an editor; exits 1 on an error |
| `gesso gen` | the code models and `queries.sql` describe, and a migration for a model's change |
| `gesso migrate new <name>` | an empty migration pair |
| `gesso add module <name>` | a CRUD module from `-field`s, see [Getting started](getting-started.md) |
| `gesso add endpoint <module>/<name>` | one endpoint in a module: its `Endpoint` and handler in `<name>.go`, its `Req`/`Res` in the models, its line in `Routes` |
| `gesso add auth` | sign-in, see [Identity and limits](identity-and-limits.md) |
| `gesso add guard <name>` | `internal/guards/<name>.go`, for `-guard` |
| `gesso add command <name>` | `internal/commands/<name>.go`, run as `app <name>` |
| `gesso add middleware <name>` | `internal/middleware/<name>.go`, around every route |
| `gesso ai init` | gesso's rules for coding agents in `AGENTS.md`, between markers; the rest of the file is kept |
| `gesso completion bash\|zsh\|fish` | a shell completion script |
| `gesso man [-install]` | the manual page |
| `gesso version` | the CLI's release and its Go |

```sh
gesso add endpoint docs/reorder -method PUT -path /docs-order -guard signedIn -field slugs:strings
gesso add endpoint docs/stats -method GET -res total:int
```

`add endpoint`'s `-field name:type[:rules]` and `-res name:type` take `string`,
`text`, `int`, `bool`, `float`, `time`, `date`, `strings`, `ints`; a `{param}`
in `-path` becomes a field of the Req. Without `-res` it answers nothing (204).

`routes`, `inspect` and `check` build the project and run it as
`<program> gesso-inspect`: `gesso.Main` answers that by printing the app — it
builds it, serves nothing and migrates nothing.

A mistyped command or flag is answered with the closest one:
`gesso: unknown command "chek" (did you mean "check"?)`. The exit code is 0
when all is well, 1 when a command failed or found something, 2 when it was
used wrong.

## Logs

On a terminal, `gesso.Main` logs a line per record, for a person:

```
13:50:29 • gesso: listening  addr=127.0.0.1:8080
13:50:30 ✗ request  method=GET path=/docs status=500
```

Coloured unless `NO_COLOR` is set or `TERM` is `dumb`; `FORCE_COLOR` asks for
it on a pipe. Anywhere else it is JSON on standard output, for a machine.
`gesso.TerminalLogger(w)` is the same, for a program of your own.
