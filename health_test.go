package gesso

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	dbUp := true
	a := mustNew(t, Config{BasePath: "/api", Health: &Health{
		Timeout:  50 * time.Millisecond,
		CacheFor: time.Nanosecond,
		Checks: map[string]func(context.Context) error{
			"database": func(context.Context) error {
				if !dbUp {
					return errors.New("connection refused: 10.0.0.5:5432")
				}
				return nil
			},
			"slow": func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		},
	}})
	if w := send(a, "GET", "/api/healthz", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("live: %d %s", w.Code, w.Body)
	}
	w := send(a, "GET", "/api/readyz", nil)
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"database":"ok"`) || !strings.Contains(w.Body.String(), `"slow":"failed"`) ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("a check past its timeout: %d %s", w.Code, w.Body)
	}
	dbUp = false
	if w := send(a, "GET", "/api/readyz", nil); strings.Contains(w.Body.String(), "10.0.0.5") {
		t.Fatalf("a failed check's error is logged, not answered: %s", w.Body)
	}
	if w := send(a, "POST", "/api/healthz", nil); w.Code != 404 {
		t.Fatalf("a probe is a GET: %d", w.Code)
	}
	if w := send(a, "GET", "/healthz", nil); w.Code != 404 {
		t.Fatalf("the probes are under BasePath: %d", w.Code)
	}
}

func TestReadinessWhileDraining(t *testing.T) {
	a := mustNew(t, Config{Health: &Health{}})
	if w := send(a, "GET", "/readyz", nil); w.Code != 200 {
		t.Fatalf("ready: %d %s", w.Code, w.Body)
	}
	_ = a.Shutdown(context.Background())
	if w := send(a, "GET", "/readyz", nil); w.Code != 503 {
		t.Fatalf("shutting down: %d", w.Code)
	}
	if w := send(a, "GET", "/healthz", nil); w.Code != 200 {
		t.Fatalf("still alive: %d", w.Code)
	}
}

// Run, told to stop, answers readiness 503 for DrainDelay, still serving, and
// only then stops.
func TestDrainDelay(t *testing.T) {
	a := mustNew(t, Config{Health: &Health{}, DrainDelay: 300 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: a}
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, srv) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)
	if !a.draining.Load() {
		t.Fatal("not draining")
	}
	if w := send(a, "GET", "/readyz", nil); w.Code != 503 {
		t.Fatalf("readiness while draining: %d", w.Code)
	}
	select {
	case <-done:
		t.Fatal("Run stopped before DrainDelay")
	default:
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A readiness result is answered again for CacheFor: a probe anyone may send
// runs the checks once a second at most, however often it comes.
func TestReadinessIsCached(t *testing.T) {
	var runs int
	a := mustNew(t, Config{Health: &Health{Checks: map[string]func(context.Context) error{
		"db": func(context.Context) error { runs++; return nil },
	}}})
	for range 50 {
		if w := send(a, "GET", "/readyz", nil); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if runs != 1 {
		t.Fatalf("50 probes ran the check %d times", runs)
	}
}

// A prober that hangs up mid-check does not fail the check for everyone the
// cache answers next.
func TestReadinessIgnoresTheProbersHangUp(t *testing.T) {
	a := mustNew(t, Config{Health: &Health{Checks: map[string]func(context.Context) error{
		"db": func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Millisecond):
				return nil
			}
		},
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx)
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	a.ServeHTTP(httptest.NewRecorder(), r)
	if w := send(a, "GET", "/readyz", nil); w.Code != 200 {
		t.Fatalf("after a hung-up probe: %d %s", w.Code, w.Body)
	}
}
