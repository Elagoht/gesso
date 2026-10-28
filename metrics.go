package gesso

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics serves the app's request metrics in the Prometheus text format:
//
//	gesso_http_requests_total{method,route,status}       counter
//	gesso_http_request_duration_seconds{method,route}    histogram
//	gesso_http_requests_in_flight                         gauge
//	go_goroutines, go_memstats_heap_alloc_bytes           gauges
//
// route is the pattern a request matched — "GET /notes/{id}" — never its path,
// so the series stay as many as the routes; a request no route matched is
// "unmatched".
type Metrics struct {
	// Path, under BasePath. Default /metrics.
	Path string
	// Token, when set, is the bearer token a scrape must send: metrics say
	// what the app serves and how much. Without it, keep the path off the
	// public network.
	Token string
}

// Tracer begins a span for each request, for OpenTelemetry or any tracer the
// application brings. Start returns the ctx the request is served with — the
// span in it — and a function gesso calls with the status when the answer is
// written. route is as Metrics names it.
type Tracer interface {
	Start(r *http.Request, route string) (context.Context, func(status int))
}

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type metricKey struct{ method, route string }

type histogram struct {
	counts []uint64 // per bucket, not cumulative; the last is +Inf
	sum    float64
	n      uint64
}

type metrics struct {
	mu       sync.Mutex
	requests map[metricKey]map[int]uint64
	latency  map[metricKey]*histogram
	inFlight atomic.Int64
}

func newMetrics() *metrics {
	return &metrics{requests: map[metricKey]map[int]uint64{}, latency: map[metricKey]*histogram{}}
}

func (m *metrics) observe(method, route string, status int, d time.Duration) {
	k := metricKey{method, route}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.requests[k] == nil {
		m.requests[k] = map[int]uint64{}
	}
	m.requests[k][status]++
	h := m.latency[k]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(latencyBuckets)+1)}
		m.latency[k] = h
	}
	s := d.Seconds()
	i, _ := slices.BinarySearch(latencyBuckets, s)
	h.counts[i]++
	h.sum += s
	h.n++
}

func (m *metrics) write(w http.ResponseWriter) {
	var b strings.Builder
	m.mu.Lock()
	keys := make([]metricKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b metricKey) int {
		return strings.Compare(a.route+" "+a.method, b.route+" "+b.method)
	})
	b.WriteString("# HELP gesso_http_requests_total Requests answered, by method, route and status.\n# TYPE gesso_http_requests_total counter\n")
	for _, k := range keys {
		statuses := make([]int, 0, len(m.requests[k]))
		for s := range m.requests[k] {
			statuses = append(statuses, s)
		}
		slices.Sort(statuses)
		for _, s := range statuses {
			fmt.Fprintf(&b, "gesso_http_requests_total{method=%s,route=%s,status=\"%d\"} %d\n",
				label(k.method), label(k.route), s, m.requests[k][s])
		}
	}
	b.WriteString("# HELP gesso_http_request_duration_seconds Time to answer, by method and route.\n# TYPE gesso_http_request_duration_seconds histogram\n")
	for _, k := range keys {
		h := m.latency[k]
		var cum uint64
		for i, le := range latencyBuckets {
			cum += h.counts[i]
			fmt.Fprintf(&b, "gesso_http_request_duration_seconds_bucket{method=%s,route=%s,le=\"%s\"} %d\n",
				label(k.method), label(k.route), strconv.FormatFloat(le, 'g', -1, 64), cum)
		}
		cum += h.counts[len(latencyBuckets)]
		fmt.Fprintf(&b, "gesso_http_request_duration_seconds_bucket{method=%s,route=%s,le=\"+Inf\"} %d\n", label(k.method), label(k.route), cum)
		fmt.Fprintf(&b, "gesso_http_request_duration_seconds_sum{method=%s,route=%s} %s\n", label(k.method), label(k.route), strconv.FormatFloat(h.sum, 'g', -1, 64))
		fmt.Fprintf(&b, "gesso_http_request_duration_seconds_count{method=%s,route=%s} %d\n", label(k.method), label(k.route), h.n)
	}
	m.mu.Unlock()
	fmt.Fprintf(&b, "# HELP gesso_http_requests_in_flight Requests being answered.\n# TYPE gesso_http_requests_in_flight gauge\ngesso_http_requests_in_flight %d\n", m.inFlight.Load())
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(&b, "# HELP go_goroutines Goroutines that exist.\n# TYPE go_goroutines gauge\ngo_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(&b, "# HELP go_memstats_heap_alloc_bytes Heap bytes allocated and in use.\n# TYPE go_memstats_heap_alloc_bytes gauge\ngo_memstats_heap_alloc_bytes %d\n", ms.HeapAlloc)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

// label is a Prometheus label value, quoted and escaped.
func label(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// route is the pattern r matches, as metrics and traces name it.
func (a *App) route(r *http.Request) string {
	if _, pattern := a.mux.Handler(r); pattern != "" {
		return pattern
	}
	return "unmatched"
}

// scrape answers the metrics path, and reports whether r was for it.
func (a *App) scrape(w http.ResponseWriter, r *http.Request) bool {
	mc := a.cfg.Metrics
	if mc == nil || r.Method != http.MethodGet || r.URL.Path != a.basePath+mc.Path {
		return false
	}
	if mc.Token != "" {
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(mc.Token)) != 1 {
			a.errs.write(w, r, ErrUnauthenticated)
			return true
		}
	}
	a.metrics.write(w)
	return true
}
