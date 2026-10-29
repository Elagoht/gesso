# Modules and handlers

A module is anything with `Routes(r *gesso.Router)`. One that also has
`Init(app *gesso.App) error` is set up from the app when it is registered,
before its routes: it takes the database, adds its migrations and its
commands. No globals, no container:

```go
type Module struct{ app *gesso.App }

func New() *Module { return &Module{} }

func (m *Module) Init(app *gesso.App) error {
	m.app = app
	return nil
}

func (m *Module) Routes(r *gesso.Router) {
	gesso.Get(r, "/search", m.search)
}
```

`app.Register(modules...)` registers them — `register.go` does — and
`gesso.New(cfg, modules...)` takes them too. A module that also has
`Start(ctx) error` or `Stop(ctx) error` is started before the server listens
and stopped after it has stopped. `gesso.ModuleOf[*auth.Module](app)` finds a
registered module by its type, for another that needs it.

In `Init`, a module takes from the app:

| | |
| --- | --- |
| `app.DB()`, `app.Dialect()` | the database's pool and dialect; nil without `Config.Database` |
| `app.Migrations(fs)` | migration files, applied with the other modules' as the app starts |
| `app.Schema(fn)` | a step of its own after them: `app.Schema(queue.Migrate)` |
| `app.Constraints(map)` | the field each unique constraint is about: `{"docs.slug": "slug"}` |
| `app.Command(name, fn)` | a command: `app <name>` |

## A generated module

`gesso add module` writes a module's model, and `gesso gen` the rest of it:
see [Getting started](getting-started.md).

```
internal/notes/
  module.gen.go       Module, New, Init, Routes, the handlers, the Endpoints
  repository.gen.go   the SQL
  migrations/
  models/
    models.go           Note, //gesso:table — the source gesso gen writes the rest from
    requests.gen.go     ReqCreateNote, ReqNoteKey, ReqListNotes … — what clients send
    responses.gen.go    ResNote, ResNoteItem — what they get back
```

What a client sends is a `Req…`, what it gets back a `Res…`, in their own
files; the stored record is a third type.

The struct's doc comment holds its directives, after `//gesso:table <table>`:

| Directive | |
| --- | --- |
| `//gesso:write <guard>` | guards create, update, patch, delete and reorder (`-guard`) |
| `//gesso:read <guard>` | guards get and list (`-read`) |
| `//gesso:sortable` | the records are in the client's order (`-sortable`) |

A sortable model keeps a position column the client never sees. A new record
goes last; `PUT /<table>/order` with `{"keys": [...]}` puts the records the
caller can see in that order, and those it cannot see keep their place. A
string key may then not be `order`.

The list is a page of 20 (`?take=`, at most 100, `?after=` the cursor). With
`orderable` fields it takes `?order=<field>` or `?order=-<field>`, and the
cursor follows that order; a cursor from another order is 400 `bad_cursor`.
A `filterable` field is `?<field>=value`. When some fields are `listable`,
the list answers `Res<Entity>Item` — those fields, the id and the key — and
get answers the whole `Res<Entity>`; when none is, the list answers whole
records.

### Fields only some may see

```go
	Notes  string `gesso:"guard=admin,max=500"`
	Public bool   `gesso:"gate=admin"`
```

`guard=admin` hides `notes` from the answers to callers who do not pass the
`admin` guard; a write to it by them is 403, and their PUT keeps its value. A
guarded field is not the key, a filter, an order, the sort or a gate: they
would show it to everyone.

`gate=admin`, on a `bool`, makes a record with it false visible only to
callers who pass `admin`: to others it is 404 on get, and absent from the list,
updates, deletes and the reorder. One gate a model.

Guards are named as `gesso add guard` made them: `signedIn` is
`internal/guards/signedIn.go`. In a handler of your own,
`gesso.Allowed(ctx, guards.Admin)` asks the same.

### Hooks and routes of your own

The generated code is never edited. A file of your own in the module's
package defines what it calls, by name:

```go
func (m *Module) BeforeCreate(ctx context.Context, d *models.Doc) error
func (m *Module) BeforeUpdate(ctx context.Context, d *models.Doc) error
func (m *Module) BeforeDelete(ctx context.Context, d models.Doc) error
func (m *Module) routes(r *gesso.Router)
```

`BeforeCreate` sees the record before it is inserted; `BeforeUpdate` the
record a PUT or PATCH made, in the transaction that saves it; `BeforeDelete`
the record about to go. An error is the answer, and nothing is written:

```go
func (m *Module) BeforeDelete(ctx context.Context, d models.Doc) error {
	if d.Published != nil {
		return gesso.Conflict("doc_published", "a published doc is not deleted")
	}
	return nil
}
```

`gesso.IdentityFrom(ctx)` is who asks: a hook can fill a field from its
`Name`.

`routes` adds endpoints beside the generated ones; `gesso add endpoint`
writes it, in `routes.go`. Run `gesso gen` after adding a hook: it calls only
those it finds.

## Adding an endpoint

```sh
gesso add endpoint docs/stats -method GET -res total:int
```

writes `stats.go` with the handler, `ResStatsDocs` in `models/responses.go`,
and the route — in `routes.go` for a generated module, in `Routes` for an
empty one:

```go
func (m *Module) routes(r *gesso.Router) {
	gesso.Get(r, "/docs/stats", m.statsDocs)
}
```

`gesso.Get`, `Post`, `Put`, `Patch` and `Delete` read the input and output
types from the handler, take the same options as `Handle`, and return the
route as an `Endpoint`. For a caller in another process, declare it as an
`Endpoint` var instead, as below.

## Typed handlers

```go
type ReqGetNote struct {
	Slug  string `path:"slug"`
	Draft bool   `query:"draft"`
	Lang  string `header:"Accept-Language"`
}

func (m *Module) get(ctx context.Context, in models.ReqGetNote) (models.ResNote, error)
```

Fields come from `path`, `query`, `header` tags, and the rest from the JSON body;
a body that names a path, query or header field is refused. `default:"20"` fills
a missing query or header value. The input is validated (see
[Validation](validation.md)) before the handler runs. Out is written as JSON;
`gesso.Empty` answers 204.

## Uploads

An input with `form` tags reads a form — `multipart/form-data`, or, when it has
no file field, `application/x-www-form-urlencoded` (at most 1 MiB) — instead of
JSON:

```go
type ReqSetAvatar struct {
	UserID int64         `path:"id"`
	Title  string        `form:"title" validate:"max=100"`
	Image  *gesso.File   `form:"image" accept:"image/png,image/jpeg" maxsize:"2MiB" validate:"required"`
	Extra  []*gesso.File `form:"extra" accept:"image/*"`
}

SetAvatar.Handle(r, m.setAvatar, gesso.MaxBodyBytes(10<<20)) // the route takes up to 10 MiB
```

A file's `ContentType` is read from its first bytes, not from the client;
`accept` and `maxsize` refuse a file with 400 and the field's error. `Filename`
is the client's name for it without folders — never a path to write to. The
files live until the handler returns: store what you keep (`storage`), from
`file.Open()`. `Endpoint.Call` sends a form the same way: `gesso.NewFile(name,
data)` makes a file to send.

## Partial updates

`gesso.Optional[T]` tells a field left out from one sent as null from one sent
with a value — a PATCH needs all three:

```go
type ReqPatchNote struct {
	ID    int64                  `path:"id"`
	Title gesso.Optional[string] `json:"title,omitzero" validate:"notnull,max=200"`
	Due   gesso.Optional[date.Date] `json:"due,omitzero"`   // null clears it
}

if in.Title.Set { … in.Title.Value … }
```

Rules apply to the value when one is given; `required` asks for the field,
`notnull` refuses null. `gesso.Some(v)` and `gesso.Null[T]()` make them for a
call. `gesso add module` writes a PATCH endpoint.

## Pages

A list answers `gesso.Page[T]` — `items` and the `next` cursor — and reads the
cursor back:

```go
type ReqListNotes struct {
	After string `query:"after"`
	Take  int    `query:"take" default:"20" validate:"min=1,max=100"`
}

afterID, err := gesso.DecodeCursor[int64](in.After) // ErrInvalidCursor: 400
… WHERE id > ? ORDER BY id LIMIT ? (take+1) …
page.Next = gesso.EncodeCursor(items[take-1].ID)
```

A cursor is a position, not an offset: as fast on page 1000 as on page 1, and
rows added meanwhile do not shift it.

## Files and streams

A handler answers a file with `*gesso.Download` — `Body`, `ContentType`,
`Filename` (an attachment), `Size`, and, with a seekable Body, `ModTime` for
ranges and If-Modified-Since. Server-sent events are `gesso.SSE`:

```go
func (m *Module) watch(ctx context.Context, in models.ReqWatchNotes) (*gesso.Stream, error) {
	return gesso.SSE(func(ctx context.Context, send func(Note) error) error {
		for n := range m.changes(ctx) {
			if err := send(n); err != nil {
				return err
			}
		}
		return nil
	}), nil
}
```

## Caching and versions

A GET's 200 carries an ETag, the hash of its JSON; a request whose
If-None-Match names it is answered 304. An update guards against overwriting
a change it has not seen with the ETag the client read:

```go
if err := gesso.IfMatch(ctx, gesso.ETagOf(current)); err != nil {
	return models.ResNote{}, err // 412
}
```

## Endpoints

An `Endpoint` declares a route once, so the server and a caller share its types:

```go
var GetNote = gesso.Endpoint[models.ReqGetNote, models.ResNote]{Method: "GET", Pattern: "/notes/{slug}"}

GetNote.Handle(r, m.get)                                          // the server
note, err := GetNote.Call(ctx, gesso.NewRemote(url, nil), models.ReqGetNote{Slug: "jam"}) // a caller
```

`gesso.NewLocal(app)` calls the app in the same process through its whole
pipeline. A call refuses a path value that would leave its route (`..`), and
follows no redirect. An error the other app answered is an `*Error` the caller
can match with `errors.Is`; returned from a handler as it is, it is answered
502 — map it to pass it on.

## Errors

Every error is answered as problem+json (RFC 9457) with a stable `code`:

```go
var ErrNoteLocked = gesso.Conflict("note_locked", "the note is locked")
```

`sql.ErrNoRows` is a 404, a validation failure a 400 with its fields, a driver's
constraint violation a 409, 400 or 422 naming the field (`app.Constraints`), any
other error a logged 500 that says nothing of its cause. `gesso.Extend(err, T)`
adds members to the problem.

## The pipeline

```
Config.Middleware → App.Use → Router.Use → authenticate → guards → decode + validate → interceptors → handler
```

`Config.Middleware` reaches every module; `app.Use` the modules registered
after it — `gesso add middleware` puts it before `app.Register` in
`register.go`. `Config.Authenticator` authenticates every request; when it is
nil, a registered module that is an `Authenticator` — `auth.New()` — does,
registered before or after the others. A router's `Use`, `Authenticate`,
`Guard` and `Intercept` reach its routes and groups. `r.Group("/admin", fn)`
is a surface with a pipeline of its own.

## OpenAPI and Swagger UI

`Config.OpenAPI` makes an OpenAPI 3.1 document from the typed handlers: their
inputs, validate rules, outputs, statuses and guards. `gesso new` turns it on:

```go
OpenAPI: &gesso.OpenAPIConfig{Title: "app", Version: "0.1.0", Path: "/openapi.json", UI: "/swagger"},
```

`Path` serves the document, `UI` serves Swagger UI with the document in the
page; leave either empty to turn it off. Swagger UI's script and stylesheet come
from jsdelivr, pinned and checked by hash. Its Authorize button takes the
token `POST /auth/login` returns. To keep them behind guards, leave both empty
and register them on a router:

```go
r.Group("/internal", func(g *gesso.Router) {
    g.Guard(gesso.RequireRole("admin"))
    g.Get("/openapi.json", gesso.ServeOpenAPI())
    g.Get("/swagger", gesso.ServeSwaggerUI())
})
```

`gesso.Summary`, `gesso.Description` and `gesso.Tags` on a route describe it
in the document; without `Tags`, a route is tagged with its module's package.
