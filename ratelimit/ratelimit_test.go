package ratelimit_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/ratelimit"
	"github.com/Elagoht/gesso/ratelimit/ratelimittest"
)

func TestMemory(t *testing.T) {
	ratelimittest.Run(t, func() func(*testing.T) ratelimit.Store {
		shared := map[*testing.T]*ratelimit.Memory{}
		return func(t *testing.T) ratelimit.Store {
			if m, ok := shared[t]; ok {
				return m
			}
			shared[t] = ratelimit.NewMemory(0)
			return shared[t]
		}
	}())
}

func TestMemoryForgets(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	m := ratelimit.NewMemory(2)
	take := func(key string, at time.Time) bool {
		ok, _, _ := m.Take(ctx, key, at, time.Hour, 1)
		return ok
	}
	take("a", now)
	take("b", now.Add(time.Second))
	take("c", now.Add(2*time.Second)) // forgets a: its arrival time is the earliest
	if !take("a", now.Add(3*time.Second)) {
		t.Fatal("a was not forgotten")
	}
	if take("c", now.Add(3*time.Second)) {
		t.Fatal("c, still limited, was forgotten")
	}
	// At the bound, a new client costs the same, however large the bound.
	big := ratelimit.NewMemory(100_000)
	for i := range 100_000 {
		_, _, _ = big.Take(ctx, strconv.Itoa(i), now, time.Hour, 1)
	}
	start := time.Now()
	for i := range 1000 {
		_, _, _ = big.Take(ctx, "new"+strconv.Itoa(i), now, time.Hour, 1)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("1000 new clients at a full bound of 100000 took %v", d)
	}
}

// An IPv6 client counts by its /64: picking another address in it does not
// buy more requests.
func TestIPv6CountsBySlash64(t *testing.T) {
	l := ratelimit.New(ratelimit.Options{Requests: 1, Per: time.Hour})
	h := l.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	do := func(remote string) int {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if do("[2001:db8:1:2::1]:5") != 204 || do("[2001:db8:1:2::ffff]:5") != 429 {
		t.Fatal("two addresses in one /64 counted apart")
	}
	if do("[2001:db8:1:3::1]:5") != 204 {
		t.Fatal("another /64 is another client")
	}
}

func TestMiddleware(t *testing.T) {
	c := clock.NewManual(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	l := ratelimit.New(ratelimit.Options{Requests: 1, Per: 90 * time.Second, Clock: c})
	h := l.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	do := func(remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/auth/login", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("1.2.3.4:5000"); w.Code != 204 {
		t.Fatalf("first: %d", w.Code)
	}
	w := do("1.2.3.4:5001")
	if w.Code != 429 || w.Header().Get("Retry-After") != "90" || !strings.Contains(w.Body.String(), `"too_many_requests"`) ||
		w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("second: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := do("5.6.7.8:5000"); w.Code != 204 {
		t.Fatalf("another address: %d", w.Code)
	}
}

type brokenStore struct{}

func (brokenStore) Take(context.Context, string, time.Time, time.Duration, int) (bool, time.Duration, error) {
	return false, 0, errors.New("database down")
}

type logs struct{ msgs []string }

func (l *logs) Enabled(context.Context, slog.Level) bool { return true }
func (l *logs) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logs) WithGroup(string) slog.Handler            { return l }
func (l *logs) Handle(_ context.Context, r slog.Record) error {
	l.msgs = append(l.msgs, r.Message)
	return nil
}

// A store that fails lets the request through, and says so.
func TestMiddlewareFailsOpen(t *testing.T) {
	lg := &logs{}
	l := ratelimit.New(ratelimit.Options{Requests: 1, Per: time.Minute, Store: brokenStore{}, Logger: slog.New(lg)})
	h := l.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 204 || len(lg.msgs) != 1 || !strings.Contains(lg.msgs[0], "store failed") {
		t.Fatalf("%d %v", w.Code, lg.msgs)
	}
}

func TestClientIP(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		remote, xff, want string
	}{
		{"1.2.3.4:5", "9.9.9.9", "1.2.3.4"},
		{"10.0.0.1:5", "9.9.9.9", "9.9.9.9"},
		{"10.0.0.1:5", "6.6.6.6, 9.9.9.9, 10.0.0.2", "9.9.9.9"},
		{"10.0.0.1:5", "", "10.0.0.1"},
		{"10.0.0.1:5", "garbage, 9.9.9.9", "9.9.9.9"},
		{"10.0.0.1:5", "9.9.9.9, garbage", "10.0.0.1"},
		{"[::1]:5", "", "::1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ratelimit.ClientIP(r, proxies); got != c.want {
			t.Errorf("%s %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
