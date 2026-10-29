package gesso

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso/date"
	"github.com/Elagoht/gesso/internal/oatest/other"
)

type Note struct {
	ID      int64      `json:"id"`
	Title   string     `json:"title" validate:"required,max=120"`
	Status  string     `json:"status" validate:"oneof=draft published"`
	Email   string     `json:"email,omitempty" validate:"omitempty,email"`
	Created time.Time  `json:"created"`
	Edited  *time.Time `json:"edited"`
	Tags    []string   `json:"tags" validate:"max=5"`
	Parent  *Note      `json:"parent,omitempty"`
	Other   other.Note `json:"other"`
	Score   uint8      `json:"score"`
	Due     date.Date  `json:"due" validate:"required"`
}

type Listing[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

type listNotesIn struct {
	Take  int      `query:"take" default:"20" validate:"min=1,max=100"`
	Tag   []string `query:"tag"`
	Trace string   `header:"X-Trace" validate:"required"`
}

type updateIn struct {
	ID    string `path:"id"`
	Title string `json:"title" validate:"required,min=1"`
}

func openAPIApp(t *testing.T) *App {
	return mustNew(t, Config{BasePath: "/api", OpenAPI: &OpenAPIConfig{Title: "Notes", Version: "1.2.0", Path: "/openapi.json"}}, moduleFunc(func(r *Router) {
		r.Get("/notes", Handle(func(context.Context, listNotesIn) (Listing[Note], error) { return Listing[Note]{}, nil },
			Summary("List notes"), Tags("notes")))
		r.Put("/notes/{id}", Handle(func(context.Context, updateIn) (Note, error) { return Note{}, nil },
			Guards(RequireIdentity)))
		r.Get("/files/{path...}", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, nil }))
		r.Get("/static/", http.NotFoundHandler())
		r.Group("/admin", func(g *Router) {
			g.Guard(RequireRole("admin"))
			g.Delete("/notes/{id}", Handle(func(context.Context, updateIn) (Empty, error) { return Empty{}, nil }))
		})
	}))
}

func TestOpenAPIDocument(t *testing.T) {
	a := openAPIApp(t)
	raw := a.OpenAPI()
	if dir := os.Getenv("GESSO_OPENAPI_OUT"); dir != "" {
		_ = os.WriteFile(dir+"/openapi.json", raw, 0o644)
	}
	var doc oaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Info.Title != "Notes" || len(doc.Servers) != 1 || doc.Servers[0].URL != "/api" {
		t.Fatalf("head: %+v", doc)
	}
	var paths []string
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	if want := []string{"/admin/notes/{id}", "/files/{path}", "/notes", "/notes/{id}"}; !slices.Equal(paths, want) {
		t.Fatalf("paths %v: the base path is in servers, a subtree is left out, a wildcard is named", paths)
	}

	list := doc.Paths["/notes"]["get"]
	if list.Summary != "List notes" || list.Security == nil || len(*list.Security) != 0 || list.Responses["200"].Content["application/json"].Schema.Ref != "#/components/schemas/Listing_Note" {
		t.Fatalf("list: %+v", list)
	}
	byName := map[string]oaParam{}
	for _, p := range list.Parameters {
		byName[p.Name] = p
	}
	take := byName["take"]
	if take.In != "query" || take.Required || string(take.Schema.Default) != "20" || *take.Schema.Minimum != 1 || *take.Schema.Maximum != 100 {
		t.Fatalf("take: %+v %+v", take, take.Schema)
	}
	if tag := byName["tag"]; tag.Schema.Type[0] != "array" || tag.Explode == nil {
		t.Fatalf("tag: %+v", tag)
	}
	if tr := byName["X-Trace"]; tr.In != "header" || !tr.Required {
		t.Fatalf("trace: %+v", tr)
	}

	put := doc.Paths["/notes/{id}"]["put"]
	if put.Security == nil || len(*put.Security) != 1 || put.Summary != "" || len(put.Tags) != 1 || put.Tags[0] != "gesso" || put.Responses["401"].Description == "" || put.RequestBody == nil {
		t.Fatalf("put: %+v", put)
	}
	body := doc.Components.Schemas[strings.TrimPrefix(put.RequestBody.Content["application/json"].Schema.Ref, "#/components/schemas/")]
	if _, ok := body.Properties["id"]; ok || body.Properties["title"] == nil || !slices.Equal(body.Required, []string{"title"}) {
		t.Fatalf("a path field is not in the body: %+v", body)
	}
	if files := doc.Paths["/files/{path}"]["get"]; len(files.Parameters) != 1 || files.Parameters[0].Name != "path" || !files.Parameters[0].Required {
		t.Fatalf("a path name the input does not read is still a parameter: %+v", files.Parameters)
	}
	if del := doc.Paths["/admin/notes/{id}"]["delete"]; len(*del.Security) != 1 || del.Responses["204"].Content != nil {
		t.Fatalf("delete: %+v", del)
	}

	note := doc.Components.Schemas["Note"]
	if note == nil {
		t.Fatalf("schemas: %v", keys(doc.Components.Schemas))
	}
	checks := map[string]bool{
		"title maxLength":    *note.Properties["title"].MaxLength == 120,
		"status enum":        len(note.Properties["status"].Enum) == 2,
		"email format":       note.Properties["email"].Format == "email",
		"created date-time":  note.Properties["created"].Format == "date-time",
		"due date":           note.Properties["due"].Format == "date" && note.Properties["due"].Ref == "",
		"edited nullable":    slices.Equal(note.Properties["edited"].Type, []string{"string", "null"}),
		"tags maxItems":      *note.Properties["tags"].MaxItems == 5,
		"parent recursive":   note.Properties["parent"].AnyOf[0].Ref == "#/components/schemas/Note",
		"other renamed":      note.Properties["other"].Ref == "#/components/schemas/other_Note",
		"uint non-negative":  *note.Properties["score"].Minimum == 0,
		"required from tags": slices.Equal(note.Required, []string{"due", "title"}),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s: %+v", name, note)
		}
	}

	rec := do(a, "GET", "/api/openapi.json", "", "")
	if rec.Code != 200 || rec.Body.String() != string(raw) {
		t.Fatalf("served: %d", rec.Code)
	}
}

func keys(m map[string]*Schema) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

type optIn struct {
	Note string `json:"note"`
}

func TestOpenAPIGenerateOnlyAndGuarded(t *testing.T) {
	a := mustNew(t, Config{OpenAPI: &OpenAPIConfig{Title: "Private"}}, moduleFunc(func(r *Router) {
		r.Authenticate(auth)
		r.Post("/notes", Handle(func(context.Context, optIn) (Empty, error) { return Empty{}, nil }))
		r.Group("/internal", func(g *Router) {
			g.Guard(RequireRole("admin"))
			g.Get("/openapi.json", ServeOpenAPI())
		})
	}))
	if a.OpenAPI() == nil {
		t.Fatal("generated")
	}
	if code := do(a, "GET", "/openapi.json", "", "").Code; code != 404 {
		t.Fatalf("not served by itself: %d", code)
	}
	if code, _, _ := withToken(a, "GET", "/internal/openapi.json", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	code, body, _ := withToken(a, "GET", "/internal/openapi.json", "ada", "")
	if code != 200 || !strings.Contains(body, `"title": "Private"`) {
		t.Fatalf("admin: %d %s", code, body)
	}
	if strings.Contains(body, "/internal/openapi.json") {
		t.Fatal("the document's own route is not in it")
	}
	var doc oaDoc
	_ = json.Unmarshal(a.OpenAPI(), &doc)
	if doc.Paths["/notes"]["post"].RequestBody.Required {
		t.Fatal("a body with no required field is optional: an empty one is accepted")
	}
	if !bodyRequired(t, openAPIApp(t), "/notes/{id}", "put") {
		t.Fatal("a body with a required field is required")
	}
}

func bodyRequired(t *testing.T, a *App, path, method string) bool {
	var doc oaDoc
	if err := json.Unmarshal(a.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Paths[path][method].RequestBody.Required
}

func TestOpenAPIOff(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(eventsModule))
	if a.OpenAPI() != nil || do(a, "GET", "/openapi.json", "", "").Code != 404 {
		t.Fatal("no document unless asked for")
	}
}

type ruled struct {
	Slug  string   `json:"slug" validate:"required,slug"`
	Code  string   `json:"code" validate:"startswith=gso_"`
	Score float64  `json:"score" validate:"gt=0,lt=10"`
	Name  string   `json:"name" validate:"gt=2,lte=8"`
	Host  string   `json:"host" validate:"hostname"`
	Tags  []string `json:"tags" validate:"unique,max=5,dive,required,max=20,alphanum"`
	Count int      `json:"count" validate:"gte=1"`
	Kind  string   `json:"kind" validate:"eq=note"`
	Seats []int    `json:"seats" validate:"dive,gt=0"`
}

func TestOpenAPIRules(t *testing.T) {
	a := mustNew(t, Config{OpenAPI: &OpenAPIConfig{Title: "R", Version: "1"}}, moduleFunc(func(r *Router) {
		r.Post("/r", Handle(func(context.Context, ruled) (Empty, error) { return Empty{}, nil }))
	}))
	var doc oaDoc
	if err := json.Unmarshal(a.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	s := doc.Components.Schemas["ruled"]
	if s == nil {
		t.Fatalf("schemas: %v", keys(doc.Components.Schemas))
	}
	p := s.Properties
	checks := map[string]bool{
		"slug pattern":       p["slug"].Pattern == "^[a-z0-9]+(-[a-z0-9]+)*$",
		"startswith pattern": p["code"].Pattern == "^gso_",
		"gt exclusive":       p["score"].ExclusiveMinimum != nil && *p["score"].ExclusiveMinimum == 0,
		"lt exclusive":       p["score"].ExclusiveMaximum != nil && *p["score"].ExclusiveMaximum == 10,
		"gt on a string":     p["name"].MinLength != nil && *p["name"].MinLength == 3 && *p["name"].MaxLength == 8,
		"hostname format":    p["host"].Format == "hostname",
		"unique items":       p["tags"].UniqueItems && *p["tags"].MaxItems == 5,
		"dive to items":      p["tags"].Items.MaxLength != nil && *p["tags"].Items.MaxLength == 20 && p["tags"].Items.Pattern == "^[A-Za-z0-9]+$",
		"gte minimum":        *p["count"].Minimum == 1,
		"eq enum":            len(p["kind"].Enum) == 1 && string(p["kind"].Enum[0]) == `"note"`,
		"dive number":        p["seats"].Items.ExclusiveMinimum != nil && p["seats"].MinItems == nil,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s: %+v", name, p)
		}
	}
}
