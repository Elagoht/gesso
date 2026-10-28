package gesso

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func newReq(method, target, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// headerAuth knows one token per subject, from the X-Token header.
type headerAuth map[string]Identity

func (a headerAuth) Authenticate(r *http.Request) (Identity, bool, error) {
	tok := r.Header.Get("X-Token")
	if tok == "" {
		return Identity{}, false, nil
	}
	id, ok := a[tok]
	if !ok {
		return Identity{}, false, ErrInvalidToken
	}
	return id, true, nil
}

var auth = headerAuth{"ada": {Subject: "ada", Roles: []string{"admin"}}, "bob": {Subject: "bob"}}

type whoOut struct {
	Subject string `json:"subject"`
}

func who(ctx context.Context, _ Empty) (whoOut, error) {
	id, _ := IdentityFrom(ctx)
	return whoOut{Subject: id.Subject}, nil
}

func withToken(h http.Handler, method, target, token, body string) (int, string, http.Header) {
	req := newReq(method, target, body)
	if token != "" {
		req.Header.Set("X-Token", token)
	}
	rec := serve(h, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func pipelineApp(t *testing.T) *App {
	return mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Authenticate(auth)
		r.Get("/public/who", Handle(who))
		r.Post("/events", Handle(createEventPlain, Guards(RequireIdentity)))
		r.Group("/admin", func(g *Router) {
			g.Guard(RequireRole("admin"))
			g.Get("/who", Handle(who))
			g.Get("/raw", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
		})
	}))
}

type plainIn struct {
	Title string `json:"title" validate:"required"`
}

func createEventPlain(context.Context, plainIn) (Empty, error) { return Empty{}, nil }

func TestAnonymousMeetsTheGuardBeforeValidation(t *testing.T) {
	a := pipelineApp(t)
	code, body, hdr := withToken(a, "POST", "/events", "", `{"title":""}`)
	if code != 401 || !strings.Contains(body, `"unauthenticated"`) || strings.Contains(body, `"errors"`) {
		t.Fatalf("%d %s: an anonymous caller learns nothing of the input", code, body)
	}
	if hdr.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("401 names its scheme: %v", hdr)
	}
	if code, body, _ := withToken(a, "POST", "/events", "bob", `{"title":""}`); code != 400 || !strings.Contains(body, "title") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAuthentication(t *testing.T) {
	a := pipelineApp(t)
	if code, body, _ := withToken(a, "GET", "/public/who", "", ""); code != 200 || !strings.Contains(body, `"subject":""`) {
		t.Fatalf("no credential is anonymous: %d %s", code, body)
	}
	if code, body, _ := withToken(a, "GET", "/public/who", "ada", ""); code != 200 || !strings.Contains(body, `"subject":"ada"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body, _ := withToken(a, "GET", "/public/who", "forged", ""); code != 401 || !strings.Contains(body, `"invalid_token"`) {
		t.Fatalf("a bad credential is never anonymous: %d %s", code, body)
	}
}

func TestGroupGuardsCoverPlainHandlers(t *testing.T) {
	a := pipelineApp(t)
	for _, path := range []string{"/admin/who", "/admin/raw"} {
		if code, _, _ := withToken(a, "GET", path, "", ""); code != 401 {
			t.Errorf("%s anonymous: %d", path, code)
		}
		if code, _, _ := withToken(a, "GET", path, "bob", ""); code != 403 {
			t.Errorf("%s without the role: %d", path, code)
		}
		if code, _, _ := withToken(a, "GET", path, "ada", ""); code != 200 {
			t.Errorf("%s with the role: %d", path, code)
		}
	}
}

func TestInterceptors(t *testing.T) {
	var trail []string
	errUpstream := errors.New("upstream down")
	trace := func(name string) Interceptor {
		return func(ctx context.Context, call Call, next func(context.Context) error) error {
			trail = append(trail, name+" "+call.Method+" "+call.Pattern)
			err := next(ctx)
			trail = append(trail, name+" done")
			return err
		}
	}
	translate := func(ctx context.Context, _ Call, next func(context.Context) error) error {
		if err := next(ctx); errors.Is(err, errUpstream) {
			return NewError(http.StatusBadGateway, "upstream", "a service we depend on failed")
		} else {
			return err
		}
	}
	deadline := func(ctx context.Context, _ Call, next func(context.Context) error) error {
		ctx, cancel := context.WithTimeout(ctx, time.Hour)
		defer cancel()
		return next(ctx)
	}
	shared := Handle(func(ctx context.Context, _ Empty) (whoOut, error) {
		if _, ok := ctx.Deadline(); !ok {
			return whoOut{}, errors.New("the interceptor's deadline did not reach the handler")
		}
		return whoOut{Subject: "ok"}, nil
	})
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Intercept(trace("outer"), deadline)
		r.Get("/a", shared)
		r.Group("/g", func(g *Router) {
			g.Intercept(trace("inner"), translate)
			g.Get("/b", shared)
			g.Get("/fail", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, errUpstream }))
		})
	}))
	if code, body, _ := withToken(a, "GET", "/a", "", ""); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if want := []string{"outer GET /a", "outer done"}; !reflect.DeepEqual(trail, want) {
		t.Fatalf("one handler on two routes keeps each route's interceptors: %v", trail)
	}
	trail = nil
	withToken(a, "GET", "/g/b", "", "")
	if want := []string{"outer GET /g/b", "inner GET /g/b", "inner done", "outer done"}; !reflect.DeepEqual(trail, want) {
		t.Fatalf("got %v", trail)
	}
	if code, body, _ := withToken(a, "GET", "/g/fail", "", ""); code != 502 || !strings.Contains(body, `"upstream"`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestPipelineSetUpLateIsAnError(t *testing.T) {
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	_, err := New(Config{Logger: quiet()}, moduleFunc(func(r *Router) {
		r.Get("/a", ok)
		r.Guard(RequireIdentity)
		r.Authenticate(auth)
		r.Intercept(func(ctx context.Context, _ Call, next func(context.Context) error) error { return next(ctx) })
		r.Group("/g", func(g *Router) { g.Guard(nil) })
	}))
	for _, want := range []string{"Guard under", "Authenticate under", "Intercept under", "nil guard"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

type retry struct {
	RetryAfter int `json:"retryAfter"`
}

func TestProblemExtensions(t *testing.T) {
	errLimited := NewError(http.StatusTooManyRequests, "rate_limited", "slow down")
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Get("/x", Handle(func(context.Context, Empty) (Empty, error) {
			return Empty{}, Extend(errLimited, retry{RetryAfter: 30}).WithDetail("try in 30s").Wrap(errors.New("cause"))
		}))
	}))
	rec := do(a, "GET", "/x", "", "")
	p := problemOf(t, rec)
	if p.Status != 429 || p.Code != "rate_limited" || p.Detail != "try in 30s" || !strings.Contains(rec.Body.String(), `"retryAfter":30`) {
		t.Fatalf("got %s", rec.Body)
	}
	if !errors.Is(Extend(errLimited, retry{}), errLimited) {
		t.Fatal("an extended copy matches its declaration")
	}
	for name, fn := range map[string]func(){
		"not an object": func() { Extend(errLimited, 5) },
		"redefines": func() {
			Extend(errLimited, struct {
				Status string `json:"status"`
			}{"x"})
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: want a panic", name)
				}
			}()
			fn()
		}()
	}
}

// Config.Authenticator and Config.Middleware reach every module, the app's
// middleware outside a module's own; a module may replace the authenticator.
func TestAppWideAuthenticatorAndMiddleware(t *testing.T) {
	var trail []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trail = append(trail, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	partner := headerAuth{"key": {Subject: "partner"}}
	a := mustNew(t, Config{Authenticator: auth, Middleware: []Middleware{mark("app")}},
		moduleFunc(func(r *Router) {
			r.Use(mark("notes"))
			r.Get("/notes/who", Handle(who, Guards(RequireIdentity)))
		}),
		moduleFunc(func(r *Router) { r.Get("/events/who", Handle(who)) }),
		moduleFunc(func(r *Router) {
			r.Authenticate(partner)
			r.Get("/partner/who", Handle(who))
		}),
	)
	if code, body, _ := withToken(a, "GET", "/notes/who", "ada", ""); code != 200 || !strings.Contains(body, `"ada"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body, _ := withToken(a, "GET", "/events/who", "bob", ""); code != 200 || !strings.Contains(body, `"bob"`) {
		t.Fatalf("every module reads the identity: %d %s", code, body)
	}
	if code, body, _ := withToken(a, "GET", "/partner/who", "key", ""); code != 200 || !strings.Contains(body, `"partner"`) {
		t.Fatalf("a module's own authenticator wins: %d %s", code, body)
	}
	if want := []string{"app", "notes", "app", "app"}; !slices.Equal(trail, want) {
		t.Fatalf("middleware ran %v, want %v", trail, want)
	}
	if _, err := New(Config{Logger: quiet(), Middleware: []Middleware{nil}}); err == nil {
		t.Fatal("a nil middleware is reported")
	}
}
