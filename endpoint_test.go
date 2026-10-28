package gesso

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// The contract both sides import.
type (
	fileIn struct {
		Path  string     `path:"path"`
		Take  int        `query:"take" validate:"max=50"`
		Tags  []string   `query:"tag"`
		Since *time.Time `query:"since"`
		Zero  *int       `query:"zero"`
		Trace string     `header:"X-Trace"`
		Title string     `json:"title,omitempty"`
		Count int        `json:"count"`
	}
	fileOut struct {
		Path  string   `json:"path"`
		Take  int      `json:"take"`
		Tags  []string `json:"tags"`
		Since string   `json:"since"`
		Zero  string   `json:"zero"`
		Trace string   `json:"trace"`
		Title string   `json:"title"`
		Count int      `json:"count"`
		Who   string   `json:"who"`
	}
	slugIn struct {
		Slug string `path:"slug"`
	}
)

var (
	getFile    = Endpoint[fileIn, fileOut]{Method: "POST", Pattern: "/v1/files/{path...}"}
	deleteNote = Endpoint[slugIn, Empty]{Method: "DELETE", Pattern: "/v1/notes/{slug}"}
	errLocked  = Conflict("note_locked", "the note is locked")
)

// bearerAuth is auth read from the Authorization header, as a Remote sends it.
type bearerAuth struct{}

func (bearerAuth) Authenticate(r *http.Request) (Identity, bool, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return Identity{}, false, nil
	}
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Token", tok)
	return auth.Authenticate(r2)
}

// remotes is the test app reached both ways: over HTTP and in this process.
func remotes(t *testing.T) map[string]*Remote {
	a := endpointApp(t)
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return map[string]*Remote{"remote": NewRemote(srv.URL+"/api/", srv.Client()), "local": NewLocal(a)}
}

func endpointApp(t *testing.T) *App {
	return mustNew(t, Config{BasePath: "/api"}, moduleFunc(func(r *Router) {
		r.Authenticate(bearerAuth{})
		r.Group("/v1", func(g *Router) {
			getFile.Handle(g, func(ctx context.Context, in fileIn) (fileOut, error) {
				out := fileOut{Path: in.Path, Take: in.Take, Tags: in.Tags, Trace: in.Trace, Title: in.Title, Count: in.Count}
				if in.Since != nil {
					out.Since = in.Since.UTC().Format(time.RFC3339)
				}
				if in.Zero != nil {
					out.Zero = "sent"
				}
				if id, ok := IdentityFrom(ctx); ok {
					out.Who = id.Subject
				}
				return out, nil
			})
			deleteNote.Handle(g, func(_ context.Context, in slugIn) (Empty, error) {
				if in.Slug == "locked one" {
					return Empty{}, errLocked
				}
				return Empty{}, nil
			}, Guards(RequireIdentity))
		})
	}))
}

func TestEndpointRoundTrip(t *testing.T) {
	for name, rm := range remotes(t) {
		t.Run(name, func(t *testing.T) { testRoundTrip(t, rm) })
	}
}

func testRoundTrip(t *testing.T, rm *Remote) {
	ctx := context.Background()
	since := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	zero := 0
	out, err := getFile.Call(ctx, rm, fileIn{
		Path: "a b/c?d.txt", Take: 5, Tags: []string{"x", "y z"}, Since: &since, Zero: &zero,
		Trace: "t-1", Title: "hello", Count: 0,
	}, Bearer("ada"))
	if err != nil {
		t.Fatal(err)
	}
	want := fileOut{Path: "a b/c?d.txt", Take: 5, Tags: []string{"x", "y z"}, Since: "2026-10-28T09:00:00Z",
		Zero: "sent", Trace: "t-1", Title: "hello", Who: "ada"}
	if !equalOut(out, want) {
		t.Fatalf("\n got %+v\nwant %+v", out, want)
	}
}

func equalOut(a, b fileOut) bool {
	return a.Path == b.Path && a.Take == b.Take && slices.Equal(a.Tags, b.Tags) && a.Since == b.Since &&
		a.Zero == b.Zero && a.Trace == b.Trace && a.Title == b.Title && a.Count == b.Count && a.Who == b.Who
}

func TestEndpointProblemsMatchTheirDeclaration(t *testing.T) {
	for name, rm := range remotes(t) {
		t.Run(name, func(t *testing.T) { testProblems(t, rm) })
	}
}

func testProblems(t *testing.T, rm *Remote) {
	ctx := context.Background()
	if _, err := deleteNote.Call(ctx, rm, slugIn{Slug: "free"}, Bearer("ada")); err != nil {
		t.Fatalf("an Empty answer is a 204: %v", err)
	}
	_, err := deleteNote.Call(ctx, rm, slugIn{Slug: "locked one"}, Bearer("ada"))
	if !errors.Is(err, errLocked) {
		t.Fatalf("got %v", err)
	}
	if _, err := deleteNote.Call(ctx, rm, slugIn{Slug: "free"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("got %v", err)
	}
	_, err = getFile.Call(ctx, rm, fileIn{Path: "p", Take: 99})
	if !errors.Is(err, ErrInvalidInput) || len(FieldErrors(err)) != 1 || FieldErrors(err)[0].Field != "take" {
		t.Fatalf("got %v %+v", err, FieldErrors(err))
	}
	if _, err := deleteNote.Call(ctx, rm, slugIn{}); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("an empty path parameter is refused before sending: %v", err)
	}
}

func TestEndpointOutsideItsGroup(t *testing.T) {
	_, err := New(Config{Logger: quiet()}, moduleFunc(func(r *Router) {
		r.Group("/v2", func(g *Router) {
			deleteNote.Handle(g, func(context.Context, slugIn) (Empty, error) { return Empty{}, nil })
		})
	}))
	if err == nil || !strings.Contains(err.Error(), "is not under its group") {
		t.Fatalf("got %v", err)
	}
}

func TestRemoteNonProblemError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway from a proxy", http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := deleteNote.Call(context.Background(), NewRemote(srv.URL, nil), slugIn{Slug: "x"})
	var e *Error
	if !errors.As(err, &e) || e.Status != 502 || e.Code != "http_502" || !strings.Contains(e.Detail, "proxy") {
		t.Fatalf("got %v", err)
	}
}

func TestLocalPanicsAreAnswers(t *testing.T) {
	boom := Endpoint[slugIn, Empty]{Method: "GET", Pattern: "/boom/{slug}"}
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		boom.Handle(r, func(_ context.Context, in slugIn) (Empty, error) {
			if in.Slug == "abort" {
				panic(http.ErrAbortHandler)
			}
			panic("broken")
		})
	}))
	ctx := context.Background()
	var e *Error
	if _, err := boom.Call(ctx, NewLocal(a), slugIn{Slug: "x"}); !errors.As(err, &e) || e.Status != 500 {
		t.Fatalf("a panic is the app's 500, as over the network: %v", err)
	}
	if _, err := boom.Call(ctx, NewLocal(a), slugIn{Slug: "abort"}); err == nil || errors.As(err, &e) {
		t.Fatalf("an aborted handler is a failed call, not an answer: %v", err)
	}
}

type pathIn struct {
	ID string `path:"id"`
}

type wildIn struct {
	Path string `path:"path"`
}

// A path value cannot move a call to another route: "..", ".", and in a
// wildcard such a segment or an empty one, are refused before sending; and a
// redirect is not followed with the call's credential.
func TestCallStaysOnItsRoute(t *testing.T) {
	note := Endpoint[pathIn, whoOut]{Method: "GET", Pattern: "/notes/{id}"}
	file := Endpoint[wildIn, whoOut]{Method: "GET", Pattern: "/files/{path...}"}
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		note.Handle(r, func(_ context.Context, in pathIn) (whoOut, error) { return whoOut{Subject: in.ID}, nil })
		file.Handle(r, func(_ context.Context, in wildIn) (whoOut, error) { return whoOut{Subject: in.Path}, nil })
	}))
	srv := httptest.NewServer(a)
	defer srv.Close()
	ctx := context.Background()
	for name, rm := range map[string]*Remote{"remote": NewRemote(srv.URL, nil), "local": NewLocal(a)} {
		for _, id := range []string{"..", "."} {
			if _, err := note.Call(ctx, rm, pathIn{ID: id}); err == nil || !strings.Contains(err.Error(), "leave the route") {
				t.Errorf("%s: id %q: %v", name, id, err)
			}
		}
		for _, p := range []string{"../admin", "a/../../b", "a//b", "a/./b", "a/"} {
			if _, err := file.Call(ctx, rm, wildIn{Path: p}); err == nil || !strings.Contains(err.Error(), "leave the route") {
				t.Errorf("%s: path %q: %v", name, p, err)
			}
		}
		if out, err := note.Call(ctx, rm, pathIn{ID: "a..b"}); err != nil || out.Subject != "a..b" {
			t.Errorf("%s: dots inside a value are fine: %v %v", name, out, err)
		}
		if out, err := file.Call(ctx, rm, wildIn{Path: "docs/a b.md"}); err != nil || out.Subject != "docs/a b.md" {
			t.Errorf("%s: %v %v", name, out, err)
		}
	}
	// A server that redirects: the Remote reports it, and does not follow.
	var followed bool
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed = true
		}
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	}))
	defer redir.Close()
	if _, err := note.Call(ctx, NewRemote(redir.URL, redir.Client()), pathIn{ID: "x"}, Bearer("secret")); err == nil || followed {
		t.Fatalf("a redirect was followed: %v", err)
	}
}

// Another app's error, returned as it is, is this app's 502, its detail and
// its 401 not passed on.
func TestRemoteErrorsAreBadGateway(t *testing.T) {
	leaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/notes/auth" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"status":401,"code":"invalid_token","detail":"the service token expired"}`))
			return
		}
		http.Error(w, "pq: password authentication failed dsn=postgres://app:hunter2@10.0.3.7/prod", 500)
	}))
	defer leaky.Close()
	up := NewRemote(leaky.URL, nil)
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Delete("/proxy/{slug}", Handle(func(ctx context.Context, in slugIn) (Empty, error) {
			return deleteNote.Call(ctx, up, in)
		}))
	}))
	for _, slug := range []string{"x", "auth"} {
		w := send(a, "DELETE", "/proxy/"+slug, nil)
		if w.Code != 502 || !strings.Contains(w.Body.String(), `"bad_gateway"`) || strings.Contains(w.Body.String(), "hunter2") ||
			strings.Contains(w.Body.String(), "service token") || w.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("%s: %d %v %s", slug, w.Code, w.Header(), w.Body)
		}
	}
	_, err := deleteNote.Call(context.Background(), up, slugIn{Slug: "x"})
	var e *Error
	if !errors.As(err, &e) || e.Status != 500 {
		t.Fatalf("the caller still sees what the other app said: %v", err)
	}
}

// A local call is authenticated by what it sends, not by the identity in the
// caller's ctx; and ClientAddr names the client it is made for.
func TestLocalCallIsARequestOfItsOwn(t *testing.T) {
	admin := Endpoint[Empty, whoOut]{Method: "GET", Pattern: "/admin"}
	where := Endpoint[Empty, whoOut]{Method: "GET", Pattern: "/where"}
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		admin.Handle(r, func(ctx context.Context, _ Empty) (whoOut, error) { return whoOut{Subject: "in"}, nil }, Guards(RequireRole("admin")))
		where.Handle(r, func(ctx context.Context, _ Empty) (whoOut, error) {
			return whoOut{Subject: Request(ctx).RemoteAddr}, nil
		})
	}))
	local := NewLocal(a)
	ctx := WithIdentity(context.Background(), Identity{Subject: "root", Roles: []string{"admin"}})
	if _, err := admin.Call(ctx, local, Empty{}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("the caller's identity went along: %v", err)
	}
	if out, _ := where.Call(ctx, local, Empty{}, ClientAddr("203.0.113.9")); out.Subject != "203.0.113.9:0" {
		t.Fatalf("ClientAddr: %q", out.Subject)
	}
	if out, _ := where.Call(ctx, local, Empty{}); out.Subject != "127.0.0.1:0" {
		t.Fatalf("no ClientAddr: %q", out.Subject)
	}
	if out, _ := where.Call(ctx, local, Empty{}, ClientAddr("not an ip")); out.Subject != "127.0.0.1:0" {
		t.Fatalf("a bad ClientAddr: %q", out.Subject)
	}
}
