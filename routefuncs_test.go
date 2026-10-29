package gesso

import (
	"context"
	"testing"
)

type shortIn struct {
	ID string `path:"id"`
}

type shortOut struct {
	ID      string `json:"id"`
	Allowed bool   `json:"allowed"`
}

func TestShortRoutes(t *testing.T) {
	var ep Endpoint[shortIn, shortOut]
	get := func(ctx context.Context, in shortIn) (shortOut, error) {
		return shortOut{ID: in.ID, Allowed: Allowed(ctx, RequireRole("admin"))}, nil
	}
	a := mustNew(t, Config{Authenticator: auth}, moduleFunc(func(r *Router) {
		r.Group("/v1", func(g *Router) {
			ep = Get(g, "/things/{id}", get)
			Delete(g, "/things/{id}", func(context.Context, shortIn) (Empty, error) { return Empty{}, nil }, Guards(RequireIdentity))
		})
	}))
	if ep.Method != "GET" || ep.Pattern != "/v1/things/{id}" {
		t.Fatalf("endpoint %+v", ep)
	}
	if code, body, _ := withToken(a, "GET", "/v1/things/7", "ada", ""); code != 200 || body != `{"id":"7","allowed":true}`+"\n" {
		t.Fatalf("%d %q", code, body)
	}
	if _, body, _ := withToken(a, "GET", "/v1/things/7", "bob", ""); body != `{"id":"7","allowed":false}`+"\n" {
		t.Fatalf("bob %q", body)
	}
	if code, _, _ := withToken(a, "DELETE", "/v1/things/7", "", ""); code != 401 {
		t.Fatalf("guarded: %d", code)
	}
	if Allowed(context.Background(), RequireIdentity) {
		t.Fatal("outside a handler nothing is allowed")
	}
}
