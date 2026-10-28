package gesso

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Elagoht/gesso/date"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// moduleFunc is a module made of a function, for tests.
type moduleFunc func(r *Router)

func (f moduleFunc) Routes(r *Router) { f(r) }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mustNew(t *testing.T, cfg Config, mods ...Module) *App {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = quiet()
	}
	a, err := New(cfg, mods...)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func do(h http.Handler, method, target, contentType, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q, body %s", ct, rec.Body)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

type listIn struct {
	Org   string     `path:"org"`
	Take  int        `query:"take" default:"20" validate:"min=1,max=100"`
	Tags  []string   `query:"tag"`
	Since *time.Time `query:"since"`
	Agent string     `header:"X-Agent"`
}

type listOut struct {
	Org   string   `json:"org"`
	Take  int      `json:"take"`
	Tags  []string `json:"tags"`
	Since string   `json:"since"`
	Agent string   `json:"agent"`
}

func listEvents(_ context.Context, in listIn) (listOut, error) {
	out := listOut{Org: in.Org, Take: in.Take, Tags: in.Tags, Agent: in.Agent}
	if in.Since != nil {
		out.Since = in.Since.Format(time.DateOnly)
	}
	return out, nil
}

type createIn struct {
	Org   string `path:"org"`
	Title string `json:"title" validate:"required,max=10"`
	Seats int    `json:"seats" validate:"min=1"`
}

type created struct {
	ID string `json:"id"`
}

var errFull = Conflict("event_full", "the event is full")

func createEvent(ctx context.Context, in createIn) (created, error) {
	switch in.Title {
	case "full":
		return created{}, fmt.Errorf("booking: %w", errFull)
	case "missing":
		return created{}, sql.ErrNoRows
	case "boom":
		return created{}, errors.New("secret connection string leaked")
	case "panic":
		panic("handler panicked")
	}
	ResponseHeader(ctx).Set("Location", "/events/1")
	return created{ID: in.Org + "-1"}, nil
}

func eventsModule(r *Router) {
	r.Get("/orgs/{org}/events", Handle(listEvents))
	r.Post("/orgs/{org}/events", Handle(createEvent, Status(http.StatusCreated)))
	r.Delete("/orgs/{org}/events/{id}", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, nil }))
}

func TestDecodePathQueryHeader(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(eventsModule))
	req := httptest.NewRequest("GET", "/orgs/ose/events?take=5&tag=a&tag=b&since=2026-10-28T10:00:00Z", nil)
	req.Header.Set("X-Agent", "test")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out listOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	want := listOut{Org: "ose", Take: 5, Tags: []string{"a", "b"}, Since: "2026-10-28", Agent: "test"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %+v", out)
	}
}

func TestDefaultsFillAbsentValues(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(eventsModule))
	rec := do(a, "GET", "/orgs/ose/events", "", "")
	var out listOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Take != 20 || out.Tags != nil || out.Since != "" {
		t.Fatalf("got %+v", out)
	}
}

func TestBoundsRejectRatherThanClamp(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(eventsModule))
	p := problemOf(t, do(a, "GET", "/orgs/ose/events?take=500", "", ""))
	if p.Status != 400 || p.Code != "invalid_input" || len(p.Errors) != 1 || p.Errors[0].Field != "take" || p.Errors[0].Code != "max" {
		t.Fatalf("got %+v", p)
	}
	p = problemOf(t, do(a, "GET", "/orgs/ose/events?take=many&since=yesterday", "", ""))
	if len(p.Errors) != 2 || p.Errors[0].Code != "type" || p.Errors[0].Message != "must be an integer" {
		t.Fatalf("got %+v", p)
	}
}

func TestBody(t *testing.T) {
	a := mustNew(t, Config{MaxBodyBytes: 64}, moduleFunc(eventsModule))

	rec := do(a, "POST", "/orgs/ose/events", "application/json; charset=utf-8", `{"title":"Jam","seats":3}`)
	if rec.Code != 201 || rec.Header().Get("Location") != "/events/1" || strings.TrimSpace(rec.Body.String()) != `{"id":"ose-1"}` {
		t.Fatalf("status %d, headers %v, body %s", rec.Code, rec.Header(), rec.Body)
	}

	cases := []struct {
		name, ct, body string
		status         int
		code, field    string
	}{
		{"validation names json fields", "application/json", `{"title":"","seats":0}`, 400, "invalid_input", "title"},
		{"unknown field", "application/json", `{"title":"a","seats":1,"admin":true}`, 400, "invalid_input", "admin"},
		{"wrong type", "application/json", `{"title":5}`, 400, "invalid_input", "title"},
		{"broken json", "application/json", `{"title":`, 400, "invalid_json", ""},
		{"two values", "application/json", `{"title":"a","seats":1}{}`, 400, "invalid_json", ""},
		{"not json", "text/plain", `title=a`, 415, "unsupported_media_type", ""},
		{"too large", "application/json", `{"title":"` + strings.Repeat("a", 100) + `"}`, 413, "body_too_large", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := problemOf(t, do(a, "POST", "/orgs/ose/events", c.ct, c.body))
			if p.Status != c.status || p.Code != c.code {
				t.Fatalf("got %+v", p)
			}
			if c.field != "" && (len(p.Errors) == 0 || p.Errors[0].Field != c.field) {
				t.Fatalf("want field %q, got %+v", c.field, p.Errors)
			}
		})
	}

	// An empty body is no body: the rules on its fields still answer.
	p := problemOf(t, do(a, "POST", "/orgs/ose/events", "", ""))
	if p.Code != "invalid_input" || len(p.Errors) != 2 {
		t.Fatalf("got %+v", p)
	}
}

func TestErrors(t *testing.T) {
	var logs strings.Builder
	a := mustNew(t, Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}, moduleFunc(eventsModule))
	post := func(title string) Problem {
		return problemOf(t, do(a, "POST", "/orgs/ose/events", "application/json", `{"title":"`+title+`","seats":1}`))
	}
	if p := post("full"); p.Status != 409 || p.Code != "event_full" || p.Detail != "the event is full" {
		t.Fatalf("wrapped declared error: %+v", p)
	}
	if p := post("missing"); p.Status != 404 || p.Code != "not_found" {
		t.Fatalf("sql.ErrNoRows: %+v", p)
	}
	p := post("boom")
	if p.Status != 500 || p.Code != "internal" || strings.Contains(p.Detail, "secret") {
		t.Fatalf("unknown error: %+v", p)
	}
	if !strings.Contains(logs.String(), "secret connection string leaked") {
		t.Fatalf("the cause belongs in the log: %s", logs.String())
	}
	if p := post("panic"); p.Status != 500 || !strings.Contains(logs.String(), "handler panicked") {
		t.Fatalf("panic: %+v", p)
	}
	if !errors.Is(fmt.Errorf("x: %w", errFull.Wrap(io.EOF)), errFull) {
		t.Fatal("a wrapped copy matches its declaration")
	}
}

func TestErrorMappers(t *testing.T) {
	errLegacy := errors.New("legacy: locked")
	mapper := func(err error) (*Error, bool) {
		if errors.Is(err, errLegacy) {
			return NewError(http.StatusLocked, "locked", "try later"), true
		}
		return nil, false
	}
	a := mustNew(t, Config{ErrorMappers: []ErrorMapper{mapper}}, moduleFunc(func(r *Router) {
		r.Get("/x", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, errLegacy }))
	}))
	if p := problemOf(t, do(a, "GET", "/x", "", "")); p.Status != 423 || p.Code != "locked" {
		t.Fatalf("got %+v", p)
	}
}

func TestEmptyIsNoContent(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(eventsModule))
	rec := do(a, "DELETE", "/orgs/ose/events/7", "", "")
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
}

func TestRouterAnswersAreProblems(t *testing.T) {
	a := mustNew(t, Config{BasePath: "/api/"}, moduleFunc(eventsModule))
	if p := problemOf(t, do(a, "GET", "/api/nowhere", "", "")); p.Status != 404 || p.Instance != "/api/nowhere" {
		t.Fatalf("got %+v", p)
	}
	rec := do(a, "PUT", "/api/orgs/ose/events", "", "")
	if p := problemOf(t, rec); p.Status != 405 || !strings.Contains(rec.Header().Get("Allow"), "POST") {
		t.Fatalf("got %+v, allow %q", p, rec.Header().Get("Allow"))
	}
	// Outside the base path is outside the app.
	if rec := do(a, "GET", "/orgs/ose/events", "", ""); rec.Code != 404 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := do(a, "GET", "/api/orgs/ose/events", "", ""); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestRedirectsPassThrough(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Get("/files/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	}))
	rec := do(a, "GET", "/files", "", "")
	if rec.Code/100 != 3 || rec.Header().Get("Location") != "/files/" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func tag(name string, trail *[]string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*trail = append(*trail, name)
			next.ServeHTTP(w, r)
		})
	}
}

func TestGroupsAreSurfaces(t *testing.T) {
	var trail []string
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Use(tag("root", &trail))
		r.Get("/public", ok)
		r.Group("/admin", func(g *Router) {
			g.Use(tag("admin-1", &trail), tag("admin-2", &trail))
			g.Get("/users", ok)
		})
	}))
	do(a, "GET", "/admin/users", "", "")
	if !reflect.DeepEqual(trail, []string{"root", "admin-1", "admin-2"}) {
		t.Fatalf("got %v", trail)
	}
	trail = nil
	do(a, "GET", "/public", "", "")
	if !reflect.DeepEqual(trail, []string{"root"}) {
		t.Fatalf("a group's middleware is its own: %v", trail)
	}
}

type badInput struct {
	ID  string         `path:"id"`
	Bad map[string]int `query:"bad"`
}

func TestNewReportsEveryRouteProblem(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	_, err := New(Config{Logger: quiet()}, moduleFunc(func(r *Router) {
		r.Get("/a", ok)
		r.Use(tag("late", new([]string)))
		r.Get("/a", ok)
		r.Get("/events/{slug}", Handle(func(context.Context, createIn) (Empty, error) { return Empty{}, nil }))
		r.Get("/x/{id}", Handle(func(context.Context, badInput) (Empty, error) { return Empty{}, nil }))
		r.Get("no-slash", ok)
	}))
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		`Use under "" after its routes`,
		"conflicts with pattern",
		`path parameter {org}, which the pattern does not have`,
		"map[string]int cannot be read",
		`must begin with "/"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestHandleWithoutRouter(t *testing.T) {
	h := Handle(listEvents)
	mux := http.NewServeMux()
	mux.Handle("GET /orgs/{org}/events", h)
	rec := do(mux, "GET", "/orgs/x/events?take=3", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"take":3`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

type lifecycle struct {
	name     string
	trail    *[]string
	startErr error
}

func (l *lifecycle) Routes(*Router) {}

func (l *lifecycle) Start(context.Context) error {
	*l.trail = append(*l.trail, "start "+l.name)
	return l.startErr
}

func (l *lifecycle) Stop(context.Context) error {
	*l.trail = append(*l.trail, "stop "+l.name)
	return nil
}

func TestLifecycleOrder(t *testing.T) {
	var trail []string
	a := mustNew(t, Config{}, &lifecycle{name: "jobs", trail: &trail}, &lifecycle{name: "push", trail: &trail})
	a.OnShutdown(func(context.Context) error { trail = append(trail, "close db"); return nil })
	a.OnShutdown(func(context.Context) error { trail = append(trail, "flush log"); return nil })
	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"start jobs", "start push", "stop push", "stop jobs", "flush log", "close db"}
	if !reflect.DeepEqual(trail, want) {
		t.Fatalf("got %v", trail)
	}
}

func TestFailedStartStopsWhatStarted(t *testing.T) {
	var trail []string
	a := mustNew(t, Config{},
		&lifecycle{name: "a", trail: &trail},
		&lifecycle{name: "b", trail: &trail, startErr: errors.New("no redis")},
		&lifecycle{name: "c", trail: &trail})
	err := a.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no redis") {
		t.Fatalf("got %v", err)
	}
	if want := []string{"start a", "start b", "stop a"}; !reflect.DeepEqual(trail, want) {
		t.Fatalf("got %v", trail)
	}
	// No module runs now, so no route is served: the 404 a missing route would
	// get is a 503 too.
	rec := do(a, "GET", "/anything", "", "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestServeShutsDownInOrder(t *testing.T) {
	var trail []string
	a := mustNew(t, Config{Addr: "127.0.0.1:0"}, &lifecycle{name: "jobs", trail: &trail})
	a.OnShutdown(func(context.Context) error { trail = append(trail, "close db"); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, &http.Server{Addr: "127.0.0.1:0", Handler: a}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if want := []string{"start jobs", "stop jobs", "close db"}; !reflect.DeepEqual(trail, want) {
		t.Fatalf("got %v", trail)
	}
}

type mixedIn struct {
	Take  int    `query:"take" validate:"max=10"`
	Agent string `header:"X-Agent"`
	Title string `json:"title"`
}

func TestBodyCannotFillParams(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Post("/m", Handle(func(_ context.Context, in mixedIn) (mixedIn, error) { return in, nil }))
	}))
	p := problemOf(t, do(a, "POST", "/m", "application/json", `{"title":"a","take":99999,"Agent":"forged"}`))
	if p.Status != 400 || len(p.Errors) != 2 || p.Errors[0].Field != "take" || p.Errors[1].Field != "X-Agent" || p.Errors[0].Code != "unknown" {
		t.Fatalf("got %+v", p)
	}
	rec := do(a, "POST", "/m?take=3", "application/json", `{"title":"a"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Take":3`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

type dueIn struct {
	From date.Date `query:"from"`
	Due  date.Date `json:"due" validate:"required"`
}

type dueOut struct {
	From date.Date `json:"from"`
	Due  date.Date `json:"due"`
}

// A date.Date is "2006-01-02" in a query and a body, and "required" refuses its
// zero value.
func TestDateFields(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Post("/due", Handle(func(_ context.Context, in dueIn) (dueOut, error) { return dueOut(in), nil }))
	}))
	rec := do(a, "POST", "/due?from=2026-10-01", "application/json", `{"due":"2026-10-28"}`)
	if rec.Code != 200 || rec.Body.String() != `{"from":"2026-10-01","due":"2026-10-28"}`+"\n" {
		t.Fatalf("status %d: %q", rec.Code, rec.Body)
	}
	if rec := do(a, "POST", "/due", "application/json", `{}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"due"`) {
		t.Fatalf("a missing date: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(a, "POST", "/due?from=2026-02-30", "application/json", `{"due":"2026-10-28"}`); rec.Code != 400 {
		t.Fatalf("February 30 in a query: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(a, "POST", "/due", "application/json", `{"due":"28.10.2026"}`); rec.Code != 400 {
		t.Fatalf("a date in another layout: status %d: %s", rec.Code, rec.Body)
	}
}
