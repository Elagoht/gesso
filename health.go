package gesso

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Health answers the probes a load balancer or Kubernetes sends: liveness — the
// process is up — and readiness — it can serve: its checks pass, and it is not
// shutting down.
type Health struct {
	// Checks are what readiness needs, by name: "database": pool.PingContext.
	// They run at once, each bounded by Timeout.
	Checks map[string]func(context.Context) error
	// Timeout bounds each check. Default 2s.
	Timeout time.Duration
	// CacheFor is how long a readiness result is answered again before the
	// checks run anew: a probe anyone can send must not be a way to ping the
	// database at will. Default 1s.
	CacheFor time.Duration
	// LivePath and ReadyPath are under BasePath. Default /healthz and /readyz.
	LivePath, ReadyPath string
}

// healthAnswer is a probe's body: the checks by name, "ok" or "failed" — never
// the error, which a probe's caller has no need of and an attacker would.
type healthAnswer struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// readiness keeps the last readiness result, for Health.CacheFor.
type readiness struct {
	mu     sync.Mutex
	at     time.Time
	status int
	ans    healthAnswer
}

// probe answers a request for a health path, and reports whether it was one.
func (a *App) probe(w http.ResponseWriter, r *http.Request) bool {
	h := a.cfg.Health
	if h == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	switch r.URL.Path {
	case a.basePath + h.LivePath:
		writeHealth(w, http.StatusOK, healthAnswer{Status: "ok"})
	case a.basePath + h.ReadyPath:
		if a.draining.Load() || a.failed.Load() {
			writeHealth(w, http.StatusServiceUnavailable, healthAnswer{Status: "unavailable"})
			return true
		}
		a.ready.mu.Lock()
		defer a.ready.mu.Unlock()
		if !a.ready.at.IsZero() && time.Since(a.ready.at) < h.CacheFor {
			writeHealth(w, a.ready.status, a.ready.ans)
			return true
		}
		ans := healthAnswer{Status: "ok", Checks: map[string]string{}}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, check := range h.Checks {
			wg.Go(func() {
				// Not the prober's ctx: one who hangs up mid-check must not
				// leave a failure cached for the load balancer to see.
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.Timeout)
				defer cancel()
				result := "ok"
				if err := check(ctx); err != nil {
					result = "failed"
					a.cfg.Logger.WarnContext(r.Context(), "gesso: readiness check failed", "check", name, "error", err)
				}
				mu.Lock()
				ans.Checks[name] = result
				if result != "ok" {
					ans.Status = "unavailable"
				}
				mu.Unlock()
			})
		}
		wg.Wait()
		status := http.StatusOK
		if ans.Status != "ok" {
			status = http.StatusServiceUnavailable
		}
		a.ready.at, a.ready.status, a.ready.ans = time.Now(), status, ans
		writeHealth(w, status, ans)
	default:
		return false
	}
	return true
}

func writeHealth(w http.ResponseWriter, status int, ans healthAnswer) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ans)
}
