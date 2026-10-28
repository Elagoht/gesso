package gesso

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type spans struct {
	mu   sync.Mutex
	seen []string
}

type spanKey struct{}

func (s *spans) Start(r *http.Request, route string) (context.Context, func(int)) {
	ctx := context.WithValue(r.Context(), spanKey{}, route)
	return ctx, func(status int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.seen = append(s.seen, route+" "+http.StatusText(status))
	}
}

func TestMetricsAndTracer(t *testing.T) {
	tr := &spans{}
	var inHandler string
	a := mustNew(t, Config{BasePath: "/api", Metrics: &Metrics{Token: "scrape-me"}, Tracer: tr}, moduleFunc(func(r *Router) {
		r.Get("/notes/{id}", Handle(func(ctx context.Context, in pathIn) (whoOut, error) {
			inHandler, _ = ctx.Value(spanKey{}).(string)
			if in.ID == "gone" {
				return whoOut{}, ErrNotFound
			}
			return whoOut{Subject: in.ID}, nil
		}))
	}))
	for _, p := range []string{"/api/notes/1", "/api/notes/2", "/api/notes/gone", "/api/nowhere/at/all"} {
		send(a, "GET", p, nil)
	}
	if inHandler != "GET /api/notes/{id}" {
		t.Fatalf("the handler's ctx carries the span: %q", inHandler)
	}
	if w := send(a, "GET", "/api/metrics", nil); w.Code != 401 {
		t.Fatalf("a scrape without the token: %d", w.Code)
	}
	w := send(a, "GET", "/api/metrics", map[string]string{"Authorization": "Bearer scrape-me"})
	body := w.Body.String()
	for _, want := range []string{
		`gesso_http_requests_total{method="GET",route="GET /api/notes/{id}",status="200"} 2`,
		`gesso_http_requests_total{method="GET",route="GET /api/notes/{id}",status="404"} 1`,
		`gesso_http_requests_total{method="GET",route="unmatched",status="404"} 1`,
		`gesso_http_request_duration_seconds_count{method="GET",route="GET /api/notes/{id}"} 3`,
		`le="+Inf"} 3`,
		"gesso_http_requests_in_flight 0",
		"go_goroutines ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %s", want)
		}
	}
	if strings.Contains(body, "/notes/1") {
		t.Fatal("a path, not the route, became a label")
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.seen) != 4 || tr.seen[2] != "GET /api/notes/{id} Not Found" || tr.seen[3] != "unmatched Not Found" {
		t.Fatalf("spans: %v", tr.seen)
	}
}
