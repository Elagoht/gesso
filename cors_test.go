package gesso

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func corsApp(t *testing.T, c *CORS) *App {
	return mustNew(t, Config{CORS: c}, moduleFunc(func(r *Router) {
		r.Post("/notes", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, nil }))
	}))
}

func send(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCORSPreflight(t *testing.T) {
	a := corsApp(t, &CORS{Origins: []string{"https://app.example.com"}, Credentials: true})
	ask := map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization"}
	w := send(a, "OPTIONS", "/notes", ask)
	h := w.Header()
	if w.Code != 204 || h.Get("Access-Control-Allow-Origin") != "https://app.example.com" || h.Get("Access-Control-Allow-Methods") != "POST" ||
		h.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" || h.Get("Access-Control-Allow-Credentials") != "true" ||
		h.Get("Access-Control-Max-Age") != "600" || !strings.Contains(strings.Join(h.Values("Vary"), ","), "Origin") {
		t.Fatalf("%d %v", w.Code, h)
	}
	ask["Origin"] = "https://evil.example"
	if w := send(a, "OPTIONS", "/notes", ask); w.Code != 405 || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("another origin: %d %v", w.Code, w.Header())
	}
	ask["Origin"], ask["Access-Control-Request-Method"] = "https://app.example.com", "DELETE"
	if w := send(a, "OPTIONS", "/notes", ask); w.Code != 405 || w.Header().Get("Access-Control-Allow-Methods") != "" {
		t.Fatalf("a method with no route: %d %v", w.Code, w.Header())
	}
	ask["Access-Control-Request-Method"] = "POST"
	if w := send(a, "OPTIONS", "/nowhere", ask); w.Code != 404 {
		t.Fatalf("a path with no route: %d", w.Code)
	}
}

func TestCORSActualRequest(t *testing.T) {
	a := corsApp(t, &CORS{Origins: []string{"https://app.example.com"}})
	w := send(a, "POST", "/notes", map[string]string{"Origin": "https://APP.example.com"})
	h := w.Header()
	if w.Code != 204 || h.Get("Access-Control-Allow-Origin") != "https://APP.example.com" || h.Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(h.Get("Access-Control-Expose-Headers"), "X-Request-ID") {
		t.Fatalf("%d %v", w.Code, h)
	}
	if w := send(a, "POST", "/notes", nil); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("a same-origin call gets no CORS headers")
	}
	wild := corsApp(t, &CORS{Origins: []string{"*"}})
	if w := send(wild, "POST", "/notes", map[string]string{"Origin": "https://x.example"}); w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("*: %v", w.Header())
	}
}

func TestCORSConfig(t *testing.T) {
	for _, c := range []*CORS{
		{Origins: []string{"*"}, Credentials: true},
		{Origins: []string{"app.example.com"}},
		{Origins: []string{"https://app.example.com/"}},
		{},
	} {
		if _, err := New(Config{Logger: quiet(), CORS: c}); err == nil {
			t.Errorf("%+v was accepted", c)
		}
	}
}

func TestRequestID(t *testing.T) {
	var logs bytes.Buffer
	a := mustNew(t, Config{Logger: slog.New(slog.NewTextHandler(&logs, nil)), AccessLog: true}, moduleFunc(func(r *Router) {
		r.Get("/id", Handle(func(ctx context.Context, _ Empty) (whoOut, error) { return whoOut{Subject: RequestIDFrom(ctx)}, nil }))
		r.Get("/boom", Handle(func(context.Context, Empty) (Empty, error) { panic("broken") }))
	}))
	w := send(a, "GET", "/id", map[string]string{"X-Request-ID": "chosen-by-client"})
	id := w.Header().Get("X-Request-ID")
	if len(id) != 32 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("a made id, the handler's and the header's alike: %q %s", id, w.Body)
	}
	w = send(a, "GET", "/boom", nil)
	boom := w.Header().Get("X-Request-ID")
	if !strings.Contains(logs.String(), "request_id="+boom) || !strings.Contains(logs.String(), "msg=\"request failed\"") {
		t.Fatalf("the failure is logged under the request's id:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "msg=request method=GET path=/id status=200") || !strings.Contains(logs.String(), "status=500") {
		t.Fatalf("the access log:\n%s", logs.String())
	}

	trusting := mustNew(t, Config{TrustRequestID: true}, moduleFunc(func(r *Router) {
		r.Get("/id", Handle(func(context.Context, Empty) (Empty, error) { return Empty{}, nil }))
	}))
	if got := send(trusting, "GET", "/id", map[string]string{"X-Request-ID": "lb-1234"}).Header().Get("X-Request-ID"); got != "lb-1234" {
		t.Fatalf("a trusted id: %q", got)
	}
	if got := send(trusting, "GET", "/id", map[string]string{"X-Request-ID": "bad id\nInjected: yes"}).Header().Get("X-Request-ID"); len(got) != 32 {
		t.Fatalf("an unsafe id is replaced: %q", got)
	}
	if got := send(a, "GET", "/nowhere", nil).Header().Get("X-Request-ID"); got == "" {
		t.Fatal("a 404 has an id too")
	}
}
