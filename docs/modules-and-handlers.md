# Modules and handlers

A module is anything with `Routes(r *gesso.Router)`. It gets what it needs
through its constructor — no globals, no container:

```go
type Deps struct{ DB db.DB }

func New(d Deps) *Module { return &Module{svc: NewService(newRepository(d.DB))} }

func (m *Module) Routes(r *gesso.Router) {
	GetNote.Handle(r, m.get)
	CreateNote.Handle(r, m.create, gesso.Status(201), gesso.Guards(gesso.RequireIdentity))
}
```

A module that also has `Start(ctx) error` or `Stop(ctx) error` is started before
the server listens and stopped after it has stopped.

## A module's files

```
internal/notes/
  module.go      New(Deps), Routes
  endpoints.go   GetNote = gesso.Endpoint[models.ReqGetNote, models.ResNote]{…}
  handler.go     a Req in, the service, a Res out
  service.go     the rules; repository.gen.go, the SQL
  models/
    models.go         Note, //gesso:table — the source gesso gen writes the rest from
    requests.gen.go   ReqCreateNote, ReqNoteKey, ReqListNotes … — what clients send
    responses.gen.go  ResNote, n.Response() — what they get back
```

What a client sends is a `Req…`, what it gets back a `Res…`, in their own
files; the stored record is a third type, so a column can be added to the
table without showing it to anyone. `gesso add module` writes this layout.

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
		for n := range m.svc.Changes(ctx) {
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
constraint violation a 409, 400 or 422 naming the field (through `db.Errors`), any
other error a logged 500 that says nothing of its cause. `gesso.Extend(err, T)`
adds members to the problem.

## The pipeline

```
Config.Middleware → Use → authenticate → guards → decode + validate → interceptors → handler
```

`gesso.Config.Authenticator` and `Config.Middleware` reach every module; a
router's `Use`, `Authenticate`, `Guard` and `Intercept` reach its routes and
groups. `r.Group("/admin", fn)` is a surface with a pipeline of its own.
