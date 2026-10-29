package gesso

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAPISchemeForHandleGuards(t *testing.T) {
	a := mustNew(t, Config{OpenAPI: &OpenAPIConfig{Title: "T"}}, moduleFunc(func(r *Router) {
		r.Authenticate(auth)
		r.Get("/me", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, nil }, Guards(RequireIdentity)))
	}))
	var doc oaDoc
	if err := json.Unmarshal(a.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Components.SecuritySchemes["bearer"]; !ok {
		t.Fatalf("an operation asks for bearer, so the document defines it:\n%s", a.OpenAPI())
	}
}

func TestSwaggerUI(t *testing.T) {
	a := mustNew(t, Config{BasePath: "/api", OpenAPI: &OpenAPIConfig{Title: "Notes </script>", UI: "/swagger"}}, moduleFunc(func(r *Router) {
		r.Authenticate(auth)
		r.Get("/notes", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, nil }, Summary("a </script><b>")))
		r.Group("/internal", func(g *Router) {
			g.Guard(RequireRole("admin"))
			g.Get("/swagger", ServeSwaggerUI())
		})
	}))
	rec := do(a, "GET", "/api/swagger", "", "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s", rec.Code, rec.Header())
	}
	for _, want := range []string{`integrity="sha384-`, `SwaggerUIBundle({spec: {`, `"/notes"`, "Notes &lt;/script&gt;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("no %q in\n%s", want, body)
		}
	}
	if strings.Count(body, "</script>") != 2 {
		t.Fatalf("a title or summary closed the script:\n%s", body)
	}
	if code, _, _ := withToken(a, "GET", "/api/internal/swagger", "", ""); code != 401 {
		t.Fatalf("guarded: %d", code)
	}
	if code, _, _ := withToken(a, "GET", "/api/internal/swagger", "ada", ""); code != 200 {
		t.Fatalf("admin: %d", code)
	}
	off := mustNew(t, Config{}, moduleFunc(func(r *Router) { r.Get("/swagger", ServeSwaggerUI()) }))
	if code := do(off, "GET", "/swagger", "", "").Code; code != 404 {
		t.Fatalf("no OpenAPI, no UI: %d", code)
	}
}
