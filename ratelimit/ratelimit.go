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
// with Retry-After. The state is in a Store: Memory, one per process — N
// replicas behind a load balancer then let a client through up to N times as
// often — or SQL, one table every replica shares.
package ratelimit

import (
	"context"
	"log/slog"
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
	// Store keeps the clients' state. Nil: NewMemory(10000), which each
	// process has its own of; NewSQL shares it between replicas.
	Store Store
	// Clock tells the time. Nil: the system clock.
	Clock clock.Clock
	// Logger receives a Store's errors. Nil: slog.Default().
	Logger *slog.Logger
}

// Store keeps, per key, when its bucket is next empty — the generic cell rate
// algorithm (GCRA): one instant per client, the "theoretical arrival time".
type Store interface {
	// Take takes one of burst tokens for key at now, where a token comes back
	// every interval. It reports whether it got one and, if not, how long until
	// one is due. It must be atomic: two Takes at once never both get the last
	// token.
	Take(ctx context.Context, key string, now time.Time, interval time.Duration, burst int) (ok bool, retryAfter time.Duration, err error)
}

// gcra decides a Take from a client's arrival time tat: the request is let in
// if, once counted, the bucket would not be over burst; it then moves tat on by
// one interval.
func gcra(tat, now time.Time, interval time.Duration, burst int) (next time.Time, ok bool, retryAfter time.Duration) {
	if tat.Before(now) {
		tat = now
	}
	next = tat.Add(interval)
	if over := next.Sub(now) - time.Duration(burst)*interval; over > 0 {
		return tat, false, over
	}
	return next, true, 0
}

// Limiter hands out tokens per client.
type Limiter struct {
	opts     Options
	interval time.Duration // one token per interval
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
	if opts.Store == nil {
		opts.Store = NewMemory(0)
	}
	if opts.Clock == nil {
		opts.Clock = clock.System
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Key == nil {
		opts.Key = func(r *http.Request) string { return ClientIP(r, opts.TrustedProxies) }
	}
	return &Limiter{opts: opts, interval: opts.Per / time.Duration(opts.Requests)}
}

// Allow takes a token for key. When there is none, it reports how long until
// there is.
func (l *Limiter) Allow(ctx context.Context, key string) (ok bool, retryAfter time.Duration, err error) {
	return l.opts.Store.Take(ctx, key, l.opts.Clock.Now(), l.interval, l.opts.Burst)
}

// Middleware refuses a request over its client's limit with 429 and
// Retry-After, in whole seconds. When the Store fails — the database is down —
// the request is let through and the error logged: a limiter that cannot count
// must not take the API down with it.
func (l *Limiter) Middleware() gesso.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, wait, err := l.Allow(r.Context(), l.opts.Key(r))
			if err != nil {
				l.opts.Logger.ErrorContext(r.Context(), "ratelimit: the store failed; letting the request through", "error", err)
				ok = true
			}
			if !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
				gesso.WriteProblem(w, r, gesso.ErrTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Memory keeps the clients in this process, at most a bound of them: past it,
// the ones whose bucket is full again are forgotten, or, if none is, the one
// seen longest ago.
type Memory struct {
	mu   sync.Mutex
	max  int
	tats map[string]time.Time
}

// NewMemory keeps at most max clients; zero is 10000.
func NewMemory(max int) *Memory {
	if max <= 0 {
		max = 10000
	}
	return &Memory{max: max, tats: map[string]time.Time{}}
}

// Take takes a token.
func (m *Memory) Take(_ context.Context, key string, now time.Time, interval time.Duration, burst int) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tat, seen := m.tats[key]
	if !seen && len(m.tats) >= m.max {
		m.forget(now)
	}
	next, ok, wait := gcra(tat, now, interval, burst)
	m.tats[key] = next
	return ok, wait, nil
}

// forget drops every client whose bucket is full — whose arrival time has
// passed — or, if there is none, the one with the earliest arrival time.
func (m *Memory) forget(now time.Time) {
	var oldest string
	var oldestAt time.Time
	dropped := false
	for k, tat := range m.tats {
		if !tat.After(now) {
			delete(m.tats, k)
			dropped = true
			continue
		}
		if oldest == "" || tat.Before(oldestAt) {
			oldest, oldestAt = k, tat
		}
	}
	if !dropped && oldest != "" {
		delete(m.tats, oldest)
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
