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

func remoteApp(t *testing.T) *Remote {
	a := mustNew(t, Config{BasePath: "/api"}, moduleFunc(func(r *Router) {
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
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return NewRemote(srv.URL+"/api/", srv.Client())
}

func TestEndpointRoundTrip(t *testing.T) {
	rm := remoteApp(t)
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
	rm := remoteApp(t)
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
