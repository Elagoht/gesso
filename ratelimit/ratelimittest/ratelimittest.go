// Package ratelimittest holds every ratelimit.Store to the same contract.
//
//	ratelimittest.Run(t, func(t *testing.T) ratelimit.Store { return ratelimit.NewSQL(db, migrate.Postgres) })
//
// open returns an empty store; two calls in one test may share one, as two
// replicas share a table, so the suite uses keys of its own per test.
package ratelimittest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/gesso/ratelimit"
)

// Run checks the contract.
func Run(t *testing.T, open func(t *testing.T) ratelimit.Store) {
	ctx := context.Background()
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	const interval = 12 * time.Second // 5 a minute
	take := func(t *testing.T, s ratelimit.Store, key string, at time.Time) (bool, time.Duration) {
		t.Helper()
		ok, wait, err := s.Take(ctx, key, at, interval, 2)
		if err != nil {
			t.Fatal(err)
		}
		return ok, wait
	}

	t.Run("burst, then one per interval", func(t *testing.T) {
		s := open(t)
		for i := range 2 {
			if ok, _ := take(t, s, "burst", start); !ok {
				t.Fatalf("request %d of the burst refused", i+1)
			}
		}
		if ok, wait := take(t, s, "burst", start); ok || wait != interval {
			t.Fatalf("over the burst: %v, wait %v", ok, wait)
		}
		if ok, wait := take(t, s, "burst", start.Add(11*time.Second)); ok || wait != time.Second {
			t.Fatalf("11s later: %v %v", ok, wait)
		}
		if ok, _ := take(t, s, "burst", start.Add(interval)); !ok {
			t.Fatal("one interval later a token is back")
		}
		if ok, _ := take(t, s, "burst", start.Add(interval)); ok {
			t.Fatal("only one")
		}
	})

	t.Run("an idle client has its burst, not more", func(t *testing.T) {
		s := open(t)
		take(t, s, "idle", start)
		later := start.Add(time.Hour)
		for i := range 2 {
			if ok, _ := take(t, s, "idle", later); !ok {
				t.Fatalf("request %d refused", i+1)
			}
		}
		if ok, _ := take(t, s, "idle", later); ok {
			t.Fatal("the bucket holds no more than its burst")
		}
	})

	t.Run("clients are apart", func(t *testing.T) {
		s := open(t)
		take(t, s, "a-one", start)
		take(t, s, "a-one", start)
		if ok, _ := take(t, s, "a-two", start); !ok {
			t.Fatal("another client was limited")
		}
	})

	t.Run("a refusal costs nothing", func(t *testing.T) {
		s := open(t)
		take(t, s, "refused", start)
		take(t, s, "refused", start)
		for range 5 {
			take(t, s, "refused", start)
		}
		if ok, _ := take(t, s, "refused", start.Add(interval)); !ok {
			t.Fatal("refused requests pushed the next token back")
		}
	})

	t.Run("at once, the burst and no more", func(t *testing.T) {
		a, b := open(t), open(t)
		var got atomic.Int32
		var wg sync.WaitGroup
		for i := range 20 {
			s := a
			if i%2 == 1 {
				s = b
			}
			wg.Go(func() {
				ok, _, err := s.Take(ctx, "race", start, interval, 5)
				if err != nil {
					t.Error(err)
				}
				if ok {
					got.Add(1)
				}
			})
		}
		wg.Wait()
		if n := got.Load(); n != 5 {
			t.Fatalf("%d of 20 let in at once, want the burst of 5", n)
		}
	})

	t.Run("a long key", func(t *testing.T) {
		s := open(t)
		long := string(make([]byte, 400))
		take(t, s, long, start)
		take(t, s, long, start)
		if ok, _ := take(t, s, long, start); ok {
			t.Fatal("a long key was not counted")
		}
	})
}
