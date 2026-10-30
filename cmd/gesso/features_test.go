package main

// eventsModel is the model TestNew gives a new project: every option and
// directive gen knows, a keyword column (order), and every type.
const eventsModel = `package models

import (
	"time"

	"github.com/Elagoht/gesso/date"
)

//gesso:table events
//gesso:sortable
//gesso:write signedIn
type Event struct {
	ID       int64
	Title    string     ` + "`" + `gesso:"listable,orderable,max=200"` + "`" + `
	Slug     string     ` + "`" + `gesso:"key,slug,max=100"` + "`" + `
	Body     string     ` + "`" + `gesso:"text,optional"` + "`" + `
	Order    int64      ` + "`" + `gesso:"listable,orderable"` + "`" + `
	Day      *date.Date ` + "`" + `gesso:"listable,default=@today"` + "`" + `
	Status   string     ` + "`" + `gesso:"listable,default=draft,oneof=draft live"` + "`" + `
	StartsAt *time.Time ` + "`" + `gesso:"optional"` + "`" + `
	Price    float64    ` + "`" + `gesso:"optional"` + "`" + `
	Host     string     ` + "`" + `gesso:"listable,filterable,default=@user,max=100"` + "`" + `
	Notes    string     ` + "`" + `gesso:"guard=admin,max=500"` + "`" + `
	Public   bool       ` + "`" + `gesso:"gate=admin"` + "`" + `
}
`

// eventsHook is the app's own code: gen finds the hooks and calls them.
const eventsHook = `package events

import (
	"context"
	"strings"

	"github.com/Elagoht/gesso"

	"example.com/app/internal/events/models"
)

func (m *Module) BeforeCreate(ctx context.Context, e *models.Event) error {
	if e.Price == 0 {
		e.Price = 1
	}
	return nil
}

func (m *Module) BeforeUpdate(ctx context.Context, e *models.Event) error {
	e.Title = strings.TrimSpace(e.Title)
	return nil
}

func (m *Module) BeforeDelete(ctx context.Context, e models.Event) error {
	if e.Order == 99 {
		return gesso.Unprocessable("in_use", "the event is in use")
	}
	return nil
}
`

// featuresTest drives the generated events module over HTTP, on the
// project's database, as an admin (ada), a writer (bob) and anyone.
const featuresTest = `package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/auth"

	"example.com/app/internal/events/models"
)

type caller struct {
	t     *testing.T
	app   *gesso.App
	token string
}

func (c caller) do(method, path, body string) (int, string) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.app.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (c caller) list(query string) ([]string, string) {
	c.t.Helper()
	code, body := c.do("GET", "/events"+query, "")
	if code != 200 {
		c.t.Fatalf("GET /events%s: %d %s", query, code, body)
	}
	var page gesso.Page[models.ResEventItem]
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		c.t.Fatal(err)
	}
	var slugs []string
	for _, e := range page.Items {
		slugs = append(slugs, e.Slug)
	}
	return slugs, page.Next
}

func (c caller) want(method, path, body string, status int, contains ...string) string {
	c.t.Helper()
	code, got := c.do(method, path, body)
	if code != status {
		c.t.Fatalf("%s %s %s: %d, want %d: %s", method, path, body, code, status, got)
	}
	for _, s := range contains {
		if strings.HasPrefix(s, "!") {
			if strings.Contains(got, s[1:]) {
				c.t.Fatalf("%s %s: %s has %s", method, path, got, s[1:])
			}
		} else if !strings.Contains(got, s) {
			c.t.Fatalf("%s %s: %s lacks %s", method, path, got, s)
		}
	}
	return got
}

func TestEvents(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Setenv("DATABASE_URL", filepath.Join(t.TempDir(), "events.db"))
	}
	ctx := context.Background()
	app, err := newApp(gesso.Env{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(ctx) })
	authn, _ := gesso.ModuleOf[*auth.Module](app)
	users := authn.Users().(*auth.SQL)
	login := func(name string, roles ...string) caller {
		if _, err := users.Create(ctx, name, "a long password", roles...); err != nil {
			t.Fatal(err)
		}
		s, err := auth.Login.Call(ctx, gesso.NewLocal(app), auth.ReqLogin{Username: name, Password: "a long password"})
		if err != nil {
			t.Fatal(err)
		}
		return caller{t, app, s.Token}
	}
	anyone, ada, bob := caller{t: t, app: app}, login("ada", "admin"), login("bob")

	anyone.want("POST", "/events", ` + "`" + `{"title":"Jam","slug":"jam","day":"2026-11-01"}` + "`" + `, 401)
	bob.want("POST", "/events", ` + "`" + `{"title":"Jam","slug":"jam","day":"2026-11-01","public":true}` + "`" + `, 201,
		` + "`" + `"host":"bob"` + "`" + `, ` + "`" + `"status":"draft"` + "`" + `, ` + "`" + `"price":1` + "`" + `, "!notes")
	bob.want("POST", "/events", ` + "`" + `{"title":"Sneaky","slug":"sneaky","day":"2026-11-01","notes":"x"}` + "`" + `, 403)
	ada.want("POST", "/events", ` + "`" + `{"title":"Talk","slug":"talk","notes":"private","status":"live"}` + "`" + `, 201,
		` + "`" + `"notes":"private"` + "`" + `, ` + "`" + `"day":"` + "`" + `+time.Now().Format("2006-01-02")+` + "`" + `"` + "`" + `, ` + "`" + `"status":"live"` + "`" + `)
	bob.want("POST", "/events", ` + "`" + `{"title":"Bad","slug":"bad","status":"gone"}` + "`" + `, 400, "oneof")
	bob.want("POST", "/events", ` + "`" + `{"title":"Walk","slug":"walk","day":"2026-11-03","public":true,"order":5,"host":"x","price":9.5,"startsAt":"2026-11-03T10:00:00Z"}` + "`" + `, 201)
	bob.want("POST", "/events", ` + "`" + `{"title":"Again","slug":"jam","day":"2026-11-01"}` + "`" + `, 409, ` + "`" + `"field":"slug"` + "`" + `)
	bob.want("POST", "/events", ` + "`" + `{"title":"Order","slug":"order","day":"2026-11-01","public":true}` + "`" + `, 201)
	bob.want("PUT", "/events/order", ` + "`" + `{"title":"Order 2","slug":"order","day":"2026-11-01","public":true}` + "`" + `, 200, "Order 2")
	bob.want("DELETE", "/events/order", "", 204)

	// The gate: Talk is not public, and only an admin sees it.
	if got, _ := anyone.list(""); !slices.Equal(got, []string{"jam", "walk"}) {
		t.Fatalf("anyone lists %v", got)
	}
	if got, _ := ada.list(""); !slices.Equal(got, []string{"jam", "talk", "walk"}) {
		t.Fatalf("ada lists %v", got)
	}
	bob.want("GET", "/events/talk", "", 404)
	bob.want("PATCH", "/events/talk", ` + "`" + `{"title":"Mine"}` + "`" + `, 404)
	ada.want("GET", "/events/talk", "", 200, ` + "`" + `"notes":"private"` + "`" + `)

	// The list answers the listable fields; a record, all it may see.
	anyone.want("GET", "/events", "", 200, ` + "`" + `"order":` + "`" + `, "!body", "!notes", "!price")
	bob.want("GET", "/events/walk", "", 200, ` + "`" + `"price":9.5` + "`" + `, "!notes")

	// Orders, a filter, pages.
	if got, _ := ada.list("?order=-title"); !slices.Equal(got, []string{"walk", "talk", "jam"}) {
		t.Fatalf("by title, down: %v", got)
	}
	if got, _ := ada.list("?order=-order"); got[0] != "walk" {
		t.Fatalf("by order, down: %v", got)
	}
	if got, _ := anyone.list("?host=x"); !slices.Equal(got, []string{"walk"}) {
		t.Fatalf("hosted by x: %v", got)
	}
	anyone.want("GET", "/events?order=body", "", 400)
	first, next := ada.list("?order=title&take=2")
	second, last := ada.list("?order=title&take=2&after=" + next)
	if !slices.Equal(append(first, second...), []string{"jam", "talk", "walk"}) || next == "" || last != "" {
		t.Fatalf("pages: %v %v %q", first, second, last)
	}
	ada.want("GET", "/events?take=1&after="+next, "", 400)

	// The client's order: bob moves what he sees, and Talk, which he does not,
	// keeps its place.
	bob.want("PATCH", "/events", ` + "`" + `{"order":["walk","jam"]}` + "`" + `, 204)
	if got, _ := ada.list(""); !slices.Equal(got, []string{"walk", "talk", "jam"}) {
		t.Fatalf("after bob's reorder: %v", got)
	}
	bob.want("PATCH", "/events", ` + "`" + `{"order":["walk"]}` + "`" + `, 422)
	ada.want("PATCH", "/events", ` + "`" + `{"order":["jam","talk","walk"]}` + "`" + `, 204)
	if got, _ := anyone.list(""); !slices.Equal(got, []string{"jam", "walk"}) {
		t.Fatalf("after ada's reorder: %v", got)
	}

	// A guarded field: bob may not write it, and his PUT keeps it.
	ada.want("PATCH", "/events/walk", ` + "`" + `{"notes":"kept"}` + "`" + `, 200, ` + "`" + `"notes":"kept"` + "`" + `)
	bob.want("PATCH", "/events/walk", ` + "`" + `{"notes":"mine"}` + "`" + `, 403)
	bob.want("PUT", "/events/walk", ` + "`" + `{"title":"  Walk 2 ","slug":"walk","day":"2026-11-03","public":true}` + "`" + `, 200, ` + "`" + `"title":"Walk 2"` + "`" + `, "!notes")
	ada.want("GET", "/events/walk", "", 200, ` + "`" + `"notes":"kept"` + "`" + `, ` + "`" + `"title":"Walk 2"` + "`" + `, ` + "`" + `"host":"bob"` + "`" + `, ` + "`" + `"status":"draft"` + "`" + `)
	ada.want("PATCH", "/events/talk", ` + "`" + `{"order":99}` + "`" + `, 200, ` + "`" + `"host":"ada"` + "`" + `)
	ada.want("DELETE", "/events/talk", "", 422, "in_use")

	bob.want("DELETE", "/events/jam", "", 204)
	anyone.want("GET", "/events/jam", "", 404)
	bob.want("DELETE", "/events/jam", "", 404)
}
`
