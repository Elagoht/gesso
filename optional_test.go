package gesso

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type patchNote struct {
	ID    string           `path:"id"`
	Title Optional[string] `json:"title,omitzero" validate:"required,max=5"`
	Body  Optional[string] `json:"body,omitzero" validate:"max=10"`
	Pages Optional[int]    `json:"pages,omitzero" validate:"min=1"`
	Tag   Optional[string] `json:"tag,omitzero" validate:"notnull"`
}

type patchSeen struct {
	Title, Body, Pages string
}

func describe[T comparable](o Optional[T]) string {
	switch {
	case !o.Set:
		return "absent"
	case o.Null:
		return "null"
	}
	b, _ := json.Marshal(o.Value)
	return string(b)
}

var patch = Endpoint[patchNote, patchSeen]{Method: "PATCH", Pattern: "/notes/{id}"}

func patchApp(t *testing.T) *App {
	return mustNew(t, Config{OpenAPI: &OpenAPIConfig{Title: "P", Version: "1"}}, moduleFunc(func(r *Router) {
		patch.Handle(r, func(_ context.Context, in patchNote) (patchSeen, error) {
			return patchSeen{Title: describe(in.Title), Body: describe(in.Body), Pages: describe(in.Pages)}, nil
		})
	}))
}

func TestOptional(t *testing.T) {
	a := patchApp(t)
	do := func(body string) (int, string) {
		w := send(a, "PATCH", "/notes/1", nil)
		r := httptest.NewRequest("PATCH", "/notes/1", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, body := do(`{"title":"Hi","body":null}`); code != 200 || body != `{"Title":"\"Hi\"","Body":"null","Pages":"absent"}`+"\n" {
		t.Fatalf("absent, null, value: %d %s", code, body)
	}
	cases := map[string]string{
		`{"body":"x"}`:             "title:required", // required: given …
		`{"title":null}`:           "title:required", // … and not null
		`{"title":"toolong"}`:      "title:max",
		`{"title":"ok","pages":0}`: "pages:min",
	}
	for in, want := range cases {
		field, code, _ := strings.Cut(want, ":")
		if c, body := do(in); c != 400 || !strings.Contains(body, `"field":"`+field+`"`) || !strings.Contains(body, `"code":"`+code+`"`) {
			t.Errorf("%s: %d %s", in, c, body)
		}
	}
	// encoding/json names no field for an error from a type's own
	// UnmarshalJSON: a value of the wrong type is invalid JSON, unnamed.
	if c, body := do(`{"title":"ok","pages":"many"}`); c != 400 || !strings.Contains(body, `"invalid_json"`) {
		t.Errorf("a wrong type: %d %s", c, body)
	}
	if c, body := do(`{"title":"ok","tag":null}`); c != 400 || !strings.Contains(body, `"code":"notnull"`) {
		t.Errorf("notnull: %d %s", c, body)
	}
	if c, _ := do(`{"title":"ok","pages":null}`); c != 200 {
		t.Errorf("a null with a min rule is not checked against it: %d", c)
	}
}

// A Remote sends what is set — a value or null — and leaves the rest out.
func TestOptionalThroughEndpoint(t *testing.T) {
	a := patchApp(t)
	out, err := patch.Call(context.Background(), NewLocal(a), patchNote{ID: "1", Title: Some("Yo"), Body: Null[string]()})
	if err != nil || out.Title != `"Yo"` || out.Body != "null" || out.Pages != "absent" {
		t.Fatalf("%+v %v", out, err)
	}
	_, err = patch.Call(context.Background(), NewLocal(a), patchNote{ID: "1"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nothing set: the required title is missing: %v", err)
	}
	var doc oaDoc
	_ = json.Unmarshal(a.OpenAPI(), &doc)
	s := doc.Components.Schemas["patchNote"]
	if s == nil || strings.Join(s.Properties["title"].Type, ",") != "string,null" || *s.Properties["title"].MaxLength != 5 ||
		*s.Properties["pages"].Minimum != 1 {
		t.Fatalf("schema: %+v", s)
	}
}
