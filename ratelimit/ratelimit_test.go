package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
)

func TestBucket(t *testing.T) {
	c := clock.NewManual(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	l := New(Options{Requests: 5, Per: time.Minute, Burst: 2, Clock: c})
	for i := range 2 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait != 12*time.Second {
		t.Fatalf("over the burst: %v, wait %v (one token per 12s)", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("another client has its own bucket")
	}
	c.Advance(11 * time.Second)
	if ok, wait := l.Allow("a"); ok || wait != time.Second {
		t.Fatalf("11s later: %v %v", ok, wait)
	}
	c.Advance(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("12s later a token is back")
	}
	c.Advance(time.Hour)
	for i := range 2 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("an idle client has its whole burst again, not more: %d", i)
		}
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("the bucket holds no more than Burst")
	}
}

func TestMaxClients(t *testing.T) {
	c := clock.NewManual(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	l := New(Options{Requests: 1, Per: time.Hour, MaxClients: 2, Clock: c})
	l.Allow("a")
	c.Advance(time.Second)
	l.Allow("b")
	c.Advance(time.Second)
	l.Allow("c") // forgets a, the one seen longest ago
	if len(l.buckets) != 2 || l.buckets["a"] != nil {
		t.Fatalf("buckets: %v", l.buckets)
	}
	if ok, _ := l.Allow("b"); ok {
		t.Fatal("b is still limited")
	}
	c.Advance(2 * time.Hour)
	l.Allow("d") // b and c are full again: both are forgotten, not just one
	if len(l.buckets) != 1 {
		t.Fatalf("full buckets were kept: %d", len(l.buckets))
	}
}

func TestMiddleware(t *testing.T) {
	c := clock.NewManual(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	l := New(Options{Requests: 1, Per: 90 * time.Second, Clock: c})
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

func TestClientIP(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		remote, xff, want string
	}{
		{"1.2.3.4:5", "9.9.9.9", "1.2.3.4"},                     // not a proxy: its header is ignored
		{"10.0.0.1:5", "9.9.9.9", "9.9.9.9"},                    // a proxy: the client it saw
		{"10.0.0.1:5", "6.6.6.6, 9.9.9.9, 10.0.0.2", "9.9.9.9"}, // the rightmost untrusted hop, not a forged first one
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
		if got := ClientIP(r, proxies); got != c.want {
			t.Errorf("%s %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
