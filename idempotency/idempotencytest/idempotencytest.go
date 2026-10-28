// Package idempotencytest holds every idempotency.Store to the same contract.
//
//	idempotencytest.Run(t, func(t *testing.T) idempotency.Store { return idempotency.NewSQL(db, migrate.Postgres) })
//
// open may return stores sharing one table, as replicas do: the suite uses ids
// of its own per test.
package idempotencytest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/gesso/idempotency"
)

// Run checks the contract.
func Run(t *testing.T, open func(t *testing.T) idempotency.Store) {
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	const lock, keep = time.Minute, time.Hour
	begin := func(t *testing.T, s idempotency.Store, id, fp string, at time.Time) (idempotency.State, idempotency.Answer) {
		t.Helper()
		st, a, err := s.Begin(ctx, id, fp, at, lock, keep)
		if err != nil {
			t.Fatal(err)
		}
		return st, a
	}

	t.Run("claim, run, replay", func(t *testing.T) {
		s := open(t)
		if st, _ := begin(t, s, "a1", "fp", now); st != idempotency.Claimed {
			t.Fatalf("first: %v", st)
		}
		if st, _ := begin(t, s, "a1", "fp", now); st != idempotency.Running {
			t.Fatalf("while it runs: %v", st)
		}
		want := idempotency.Answer{Status: 201, ContentType: "application/json", Location: "/orders/7", Body: []byte(`{"id":7}`)}
		if err := s.Finish(ctx, "a1", want); err != nil {
			t.Fatal(err)
		}
		st, got := begin(t, s, "a1", "fp", now.Add(time.Second))
		if st != idempotency.Done || got.Status != 201 || got.Location != "/orders/7" || string(got.Body) != `{"id":7}` || got.ContentType != want.ContentType {
			t.Fatalf("replay: %v %+v", st, got)
		}
		if st, _ := begin(t, s, "a1", "other", now); st != idempotency.Mismatch {
			t.Fatalf("another request with the key: %v", st)
		}
	})

	t.Run("abandon frees the key", func(t *testing.T) {
		s := open(t)
		begin(t, s, "b1", "fp", now)
		if err := s.Abandon(ctx, "b1"); err != nil {
			t.Fatal(err)
		}
		if st, _ := begin(t, s, "b1", "fp", now); st != idempotency.Claimed {
			t.Fatalf("after abandon: %v", st)
		}
	})

	t.Run("a dead request's lock is taken over", func(t *testing.T) {
		s := open(t)
		begin(t, s, "c1", "fp", now)
		if st, _ := begin(t, s, "c1", "fp", now.Add(lock+time.Second)); st != idempotency.Claimed {
			t.Fatalf("after its lock: %v", st)
		}
	})

	t.Run("a kept answer expires", func(t *testing.T) {
		s := open(t)
		begin(t, s, "d1", "fp", now)
		_ = s.Finish(ctx, "d1", idempotency.Answer{Status: 200, Body: []byte("x")})
		if st, _ := begin(t, s, "d1", "another", now.Add(keep+time.Second)); st != idempotency.Claimed {
			t.Fatalf("after keep, the key is free for anything: %v", st)
		}
	})

	t.Run("at once, one claims", func(t *testing.T) {
		a, b := open(t), open(t)
		var claimed atomic.Int32
		var wg sync.WaitGroup
		for i := range 10 {
			s := a
			if i%2 == 1 {
				s = b
			}
			wg.Go(func() {
				st, _, err := s.Begin(ctx, "e1", "fp", now, lock, keep)
				if err != nil {
					t.Error(err)
				}
				if st == idempotency.Claimed {
					claimed.Add(1)
				}
			})
		}
		wg.Wait()
		if n := claimed.Load(); n != 1 {
			t.Fatalf("%d of 10 claimed", n)
		}
	})
}
