package gesso

import (
	"context"
	"maps"
	"strings"
	"testing"
)

type langIn struct {
	Name string   `json:"name" validate:"min=3"`
	Tags []string `json:"tags" validate:"max=1"`
	Age  int      `json:"age" validate:"gte=18"`
}

var errNoteLocked = Conflict("note_locked", "the note is locked")

func TestLanguages(t *testing.T) {
	tr := Turkish
	tr.Details = maps.Clone(tr.Details)
	tr.Details["note_locked"] = "not kilitli"
	a := mustNew(t, Config{Languages: map[string]Language{"tr": tr}}, moduleFunc(func(r *Router) {
		r.Post("/check", Handle(func(context.Context, langIn) (Empty, error) { return Empty{}, nil }))
		r.Post("/locked", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, errNoteLocked }))
	}))
	post := func(path, lang, body string) (string, map[string][]string) {
		h := map[string]string{"Content-Type": "application/json"}
		if lang != "" {
			h["Accept-Language"] = lang
		}
		w := sendBody(a, "POST", path, h, body)
		return w.Body.String(), w.Header()
	}
	body, h := post("/check", "tr-TR,tr;q=0.9,en;q=0.8", `{"name":"ab","tags":["a","b"],"age":3}`)
	for _, want := range []string{"en az 3 karakter olmalıdır", "en fazla 1 öğe içermelidir", "en az 18 olmalıdır", "istek doğrulamadan geçemedi"} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q: %s", want, body)
		}
	}
	if h["Content-Language"][0] != "tr" || !strings.Contains(strings.Join(h["Vary"], ","), "Accept-Language") {
		t.Fatalf("headers %v", h)
	}
	if body, _ := post("/locked", "tr", ""); !strings.Contains(body, `"detail":"not kilitli"`) {
		t.Fatalf("an app's own code: %s", body)
	}
	if body, h := post("/check", "en-GB,tr;q=0.5", `{"name":"ab"}`); !strings.Contains(body, "must be at least 3 characters") || h["Content-Language"] != nil {
		t.Fatalf("English first: %s %v", body, h)
	}
	if body, _ := post("/check", "de;q=0.9,tr;q=0.3", `{"name":"ab"}`); !strings.Contains(body, "en az 3") {
		t.Fatalf("a language not configured is passed over: %s", body)
	}
	if body, _ := post("/check", "", `{"name":"ab"}`); !strings.Contains(body, "must be at least") {
		t.Fatalf("no Accept-Language: %s", body)
	}
}
