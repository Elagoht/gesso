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

## Typed handlers

```go
type GetIn struct {
	Slug  string `path:"slug"`
	Draft bool   `query:"draft"`
	Lang  string `header:"Accept-Language"`
}

func (m *Module) get(ctx context.Context, in GetIn) (Note, error)
```

Fields come from `path`, `query`, `header` tags, and the rest from the JSON body;
a body that names a path, query or header field is refused. `default:"20"` fills
a missing query or header value. The input is validated (see
[Validation](validation.md)) before the handler runs. Out is written as JSON;
`gesso.Empty` answers 204.

## Endpoints

An `Endpoint` declares a route once, so the server and a caller share its types:

```go
var GetNote = gesso.Endpoint[GetIn, Note]{Method: "GET", Pattern: "/notes/{slug}"}

GetNote.Handle(r, m.get)                                          // the server
note, err := GetNote.Call(ctx, gesso.NewRemote(url, nil), GetIn{Slug: "jam"}) // a caller
```

`gesso.NewLocal(app)` calls the app in the same process through its whole
pipeline.

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
