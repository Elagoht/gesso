package idempotency_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Elagoht/gesso/idempotency"
	"github.com/Elagoht/gesso/idempotency/idempotencytest"
)

func TestMemory(t *testing.T) {
	shared := map[*testing.T]*idempotency.Memory{}
	idempotencytest.Run(t, func(t *testing.T) idempotency.Store {
		if m, ok := shared[t]; ok {
			return m
		}
		shared[t] = idempotency.NewMemory()
		return shared[t]
	})
}

func TestMiddleware(t *testing.T) {
	var runs atomic.Int32
	status := 201
	keys := idempotency.New(idempotency.Options{Store: idempotency.NewMemory()})
	h := keys.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runs.Add(1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/orders/7")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"got":` + string(body) + `}`))
	}))
	do := func(method, key, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/orders", strings.NewReader(body))
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := do("POST", "k1", "Bearer ada", `{"n":1}`)
	again := do("POST", "k1", "Bearer ada", `{"n":1}`)
	if runs.Load() != 1 || again.Code != 201 || again.Body.String() != first.Body.String() ||
		again.Header().Get("Idempotent-Replayed") != "true" || again.Header().Get("Location") != "/orders/7" {
		t.Fatalf("runs %d, retry %d %v %s", runs.Load(), again.Code, again.Header(), again.Body)
	}
	if w := do("POST", "k1", "Bearer ada", `{"n":2}`); w.Code != 422 || !strings.Contains(w.Body.String(), "idempotency_key_reused") {
		t.Fatalf("the key with another body: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "k1", "Bearer bob", `{"n":1}`); runs.Load() != 2 || w.Header().Get("Idempotent-Replayed") != "" {
		t.Fatal("another client's key of the same name replayed ada's answer")
	}
	do("POST", "", "", `{}`)
	if runs.Load() != 3 {
		t.Fatal("a request without the key did not pass through")
	}
	do("GET", "k1", "Bearer ada", "")
	if runs.Load() != 4 {
		t.Fatal("a GET is not held by keys")
	}
	status = 503
	do("POST", "k2", "Bearer ada", `{}`)
	status = 201
	if w := do("POST", "k2", "Bearer ada", `{}`); w.Code != 201 || runs.Load() != 6 {
		t.Fatalf("a failed request's key is free for a retry: %d, %d runs", w.Code, runs.Load())
	}
	if w := do("POST", strings.Repeat("k", 256), "", `{}`); w.Code != 400 {
		t.Fatalf("a too long key: %d", w.Code)
	}
	required := idempotency.New(idempotency.Options{Store: idempotency.NewMemory(), Required: true})
	rh := required.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	w := httptest.NewRecorder()
	rh.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "idempotency_key_missing") {
		t.Fatalf("required: %d %s", w.Code, w.Body)
	}
}

// A handler that panics frees the key for a retry.
func TestPanicFreesTheKey(t *testing.T) {
	keys := idempotency.New(idempotency.Options{Store: idempotency.NewMemory()})
	boom := true
	h := keys.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if boom {
			panic("broken")
		}
		w.WriteHeader(204)
	}))
	r := func() *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
		r.Header.Set("Idempotency-Key", "p")
		return r
	}
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), r())
	}()
	boom = false
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r())
	if w.Code != 204 {
		t.Fatalf("after a panic: %d %s", w.Code, w.Body)
	}
}
