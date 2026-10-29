package gesso_test

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
)

type reqNote struct {
	ID    int64  `path:"id"`
	Title string `json:"title" validate:"required"`
	Draft bool   `query:"draft"`
}

type resNote struct {
	ID int64 `json:"id"`
}

type notesModule struct{}

func adminOnly(*http.Request) error { return nil }

func (notesModule) Routes(r *gesso.Router) {
	r.Handle("PUT", "/notes/{id}", gesso.Handle(func(context.Context, reqNote) (resNote, error) { return resNote{}, nil },
		gesso.Guards(gesso.RequireIdentity), gesso.Status(201)))
	r.Group("/admin", func(g *gesso.Router) {
		g.Guard(adminOnly, gesso.RequireRole("admin"))
		g.Handle("GET", "/stats", http.NotFoundHandler())
	})
}

func TestRoutes(t *testing.T) {
	app, err := gesso.New(gesso.Config{}, notesModule{})
	if err != nil {
		t.Fatal(err)
	}
	routes := app.Routes()
	if len(routes) != 2 {
		t.Fatalf("%+v", routes)
	}
	put := routes[0]
	if put.Method != "PUT" || put.Pattern != "/notes/{id}" || put.Module != "gesso_test.notesModule" || put.Status != 201 ||
		!slices.Equal(put.Guards, []string{"gesso.RequireIdentity"}) {
		t.Fatalf("%+v", put)
	}
	if put.Input.Name != "gesso_test.reqNote" || len(put.Input.Fields) != 3 || put.Output.Name != "gesso_test.resNote" {
		t.Fatalf("types: %+v %+v", put.Input, put.Output)
	}
	if f := put.Input.Fields[0]; f.From != "path" || f.Key != "id" {
		t.Fatalf("path field: %+v", f)
	}
	if f := put.Input.Fields[1]; f.From != "json" || f.Key != "title" || f.Validate != "required" {
		t.Fatalf("json field: %+v", f)
	}
	if f := put.Input.Fields[2]; f.From != "query" || f.Key != "draft" {
		t.Fatalf("query field: %+v", f)
	}
	// RequireRole's closure is named for it — gesso.RequireRole, or RequireRole
	// where the compiler inlined it into its caller.
	if stats := routes[1]; stats.Pattern != "/admin/stats" || len(stats.Guards) != 2 || stats.Guards[0] != "gesso_test.adminOnly" ||
		!strings.HasSuffix(stats.Guards[1], "RequireRole") || stats.Input != nil {
		t.Fatalf("%+v", stats)
	}
}

func TestRouteTypeNames(t *testing.T) {
	app, err := gesso.New(gesso.Config{}, pageModule{})
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Routes()[0].Output.Name; got != "gesso.Page[gesso_test.resNote]" {
		t.Fatalf("%q", got)
	}
}

type pageModule struct{}

func (pageModule) Routes(r *gesso.Router) {
	r.Handle("GET", "/notes", gesso.Handle(func(context.Context, gesso.Empty) (gesso.Page[resNote], error) {
		return gesso.Page[resNote]{}, nil
	}))
}

func TestTerminalLogs(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	l := gesso.TerminalLogger(&buf)
	l.With("addr", ":8080").WithGroup("req").Warn("slow", "path", "/a b", "ms", 12)
	got := buf.String()
	if _, err := time.Parse(time.TimeOnly, got[:8]); err != nil {
		t.Fatalf("no time first: %q", got)
	}
	if want := ` ! slow  addr=:8080 req.path="/a b" req.ms=12` + "\n"; got[8:] != want {
		t.Fatalf("%q, want %q", got[8:], want)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatal("colour under NO_COLOR")
	}
	l.Debug("hidden")
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatal("debug written")
	}
}
