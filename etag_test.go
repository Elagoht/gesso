package gesso

import (
	"context"
	"strings"
	"testing"
)

type versioned struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func TestETags(t *testing.T) {
	store := map[string]versioned{"1": {ID: "1", Title: "first"}}
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Get("/notes/{id}", Handle(func(_ context.Context, in pathIn) (versioned, error) { return store[in.ID], nil }))
		r.Put("/notes/{id}", Handle(func(ctx context.Context, in struct {
			ID    string `path:"id"`
			Title string `json:"title"`
		}) (versioned, error) {
			if err := IfMatch(ctx, ETagOf(store[in.ID])); err != nil {
				return versioned{}, err
			}
			store[in.ID] = versioned{ID: in.ID, Title: in.Title}
			return store[in.ID], nil
		}))
	}))
	w := send(a, "GET", "/notes/1", nil)
	tag := w.Header().Get("ETag")
	if w.Code != 200 || tag == "" || tag != ETagOf(store["1"]) {
		t.Fatalf("the GET's ETag is ETagOf the value: %q %q", tag, ETagOf(store["1"]))
	}
	if w := send(a, "GET", "/notes/1", map[string]string{"If-None-Match": tag}); w.Code != 304 || w.Body.Len() != 0 {
		t.Fatalf("not modified: %d %q", w.Code, w.Body)
	}
	if w := send(a, "GET", "/notes/1", map[string]string{"If-None-Match": `"other", W/` + tag}); w.Code != 304 {
		t.Fatalf("a list naming it, weakly: %d", w.Code)
	}
	put := func(ifMatch string) int {
		r := map[string]string{"Content-Type": "application/json"}
		if ifMatch != "" {
			r["If-Match"] = ifMatch
		}
		return sendBody(a, "PUT", "/notes/1", r, `{"title":"second"}`).Code
	}
	if code := put(tag); code != 200 {
		t.Fatalf("an update of what was read: %d", code)
	}
	if code := put(tag); code != 412 {
		t.Fatalf("an update of a stale read: %d", code)
	}
	if code := put("W/" + ETagOf(store["1"])); code != 412 {
		t.Fatalf("If-Match compares strongly: %d", code)
	}
	if code := put(""); code != 200 {
		t.Fatalf("no If-Match: %d", code)
	}
	if w := send(a, "GET", "/notes/1", map[string]string{"If-None-Match": tag}); w.Code != 200 || !strings.Contains(w.Body.String(), "second") {
		t.Fatalf("changed since: %d %s", w.Code, w.Body)
	}
}
