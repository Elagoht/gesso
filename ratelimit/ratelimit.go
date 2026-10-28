// Package ratelimit limits how often each client may call: a sign-in that
// would otherwise let a password be guessed, an endpoint that is expensive to
// answer.
//
//	limit := ratelimit.New(ratelimit.Options{Requests: 5, Per: time.Minute})
//	r.Group("/auth", func(g *gesso.Router) {
//		g.Use(limit.Middleware())
//		…
//	})
//
// Each client has a token bucket: Burst requests at once, refilled at Requests
// per Per. A request with no token left is answered 429 (ErrTooManyRequests)
// with Retry-After. The buckets are in memory, one set per process: N replicas
// behind a load balancer let a client through up to N times as often.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/clock"
)

// Options configures a Limiter.
type Options struct {
	// Requests per Per is the steady rate a client may call at. Required.
	Requests int
	Per      time.Duration
	// Burst is how many a client may send at once. Zero: Requests.
	Burst int
	// Key names the client a request comes from. Nil: its IP address, read from
	// the connection, or from X-Forwarded-For when the connection comes from
	// one of TrustedProxies.
	Key func(*http.Request) string
	// TrustedProxies are the addresses of the proxies in front of the server.
	// Only a request from one of them has its X-Forwarded-For read: anyone else
	// could write any address there and never be limited.
	TrustedProxies []netip.Prefix
	// MaxClients bounds the memory: past it, the client seen longest ago is
	// forgotten. Zero: 10000.
	MaxClients int
	// Clock tells the time. Nil: the system clock.
	Clock clock.Clock
}

// Limiter hands out tokens per client.
type Limiter struct {
	opts    Options
	every   time.Duration // one token per every
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New makes a Limiter. It panics if Requests or Per is not positive, a
// programmer's error found at startup.
func New(opts Options) *Limiter {
	if opts.Requests <= 0 || opts.Per <= 0 {
		panic("ratelimit: Requests and Per must be positive")
	}
	if opts.Burst <= 0 {
		opts.Burst = opts.Requests
	}
	if opts.MaxClients <= 0 {
		opts.MaxClients = 10000
	}
	if opts.Clock == nil {
		opts.Clock = clock.System
	}
	if opts.Key == nil {
		opts.Key = func(r *http.Request) string { return ClientIP(r, opts.TrustedProxies) }
	}
	return &Limiter{opts: opts, every: opts.Per / time.Duration(opts.Requests), buckets: map[string]*bucket{}}
}

// Allow takes a token for key. When there is none, it reports how long until
// there is.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.opts.Clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, seen := l.buckets[key]
	if !seen {
		if len(l.buckets) >= l.opts.MaxClients {
			l.forget(now)
		}
		b = &bucket{tokens: float64(l.opts.Burst), last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(float64(l.opts.Burst), b.tokens+float64(now.Sub(b.last))/float64(l.every))
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(l.every))
}

// forget drops every full bucket — a client that would be let through anyway
// — or, if none is full, the one seen longest ago.
func (l *Limiter) forget(now time.Time) {
	var oldest string
	var oldestAt time.Time
	dropped := false
	for k, b := range l.buckets {
		if b.tokens+float64(now.Sub(b.last))/float64(l.every) >= float64(l.opts.Burst) {
			delete(l.buckets, k)
			dropped = true
			continue
		}
		if oldest == "" || b.last.Before(oldestAt) {
			oldest, oldestAt = k, b.last
		}
	}
	if !dropped && oldest != "" {
		delete(l.buckets, oldest)
	}
}

// Middleware refuses a request over its client's limit with 429 and
// Retry-After, in whole seconds.
func (l *Limiter) Middleware() gesso.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, wait := l.Allow(l.opts.Key(r))
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
				gesso.WriteProblem(w, r, gesso.ErrTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP is the address r comes from: the connection's, or, when the
// connection is from a trusted proxy, the last address in X-Forwarded-For that
// is not a trusted proxy's — the one the nearest proxy saw.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !isTrusted(addr, trusted) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return host // a hop we cannot read: do not trust what is left of it
		}
		if !isTrusted(hop, trusted) {
			return hop.Unmap().String()
		}
	}
	return host
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
