// Package locktest holds every lock.Locker to the same contract.
//
//	locktest.Run(t, func(t *testing.T) lock.Locker { return lock.NewPostgres(db) })
package locktest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/gesso/lock"
)

// Run checks the contract. open returns lockers sharing one store: two calls are
// two replicas.
func Run(t *testing.T, open func(t *testing.T) lock.Locker) {
	ctx := context.Background()

	t.Run("lock, unlock, lock again", func(t *testing.T) {
		l := open(t)
		unlock, err := l.Lock(ctx, "job")
		if err != nil {
			t.Fatal(err)
		}
		if err := unlock(ctx); err != nil {
			t.Fatal(err)
		}
		if err := unlock(ctx); err != nil {
			t.Fatalf("a second unlock does nothing: %v", err)
		}
		unlock, err = l.Lock(ctx, "job")
		if err != nil {
			t.Fatal(err)
		}
		_ = unlock(ctx)
	})

	t.Run("try lock while held", func(t *testing.T) {
		a, b := open(t), open(t)
		unlock, err := a.Lock(ctx, "job")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := b.TryLock(ctx, "job"); ok || err != nil {
			t.Fatalf("taken while held: %v %v", ok, err)
		}
		if u, ok, err := b.TryLock(ctx, "other"); !ok || err != nil {
			t.Fatalf("another name is free: %v %v", ok, err)
		} else {
			_ = u(ctx)
		}
		_ = unlock(ctx)
		u, ok, err := b.TryLock(ctx, "job")
		if !ok || err != nil {
			t.Fatalf("free after unlock: %v %v", ok, err)
		}
		_ = u(ctx)
	})

	t.Run("lock waits for the holder", func(t *testing.T) {
		a, b := open(t), open(t)
		unlock, err := a.Lock(ctx, "job")
		if err != nil {
			t.Fatal(err)
		}
		var released atomic.Bool
		got := make(chan error, 1)
		go func() {
			u, err := b.Lock(ctx, "job")
			if err == nil && !released.Load() {
				err = errors.New("taken before the holder let go")
			}
			if u != nil {
				_ = u(ctx)
			}
			got <- err
		}()
		time.Sleep(150 * time.Millisecond)
		released.Store(true)
		_ = unlock(ctx)
		select {
		case err := <-got:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the waiter never got the lock")
		}
	})

	t.Run("a wait ends with its context", func(t *testing.T) {
		a, b := open(t), open(t)
		unlock, err := a.Lock(ctx, "job")
		if err != nil {
			t.Fatal(err)
		}
		defer unlock(ctx)
		wctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		if _, err := b.Lock(wctx, "job"); err == nil {
			t.Fatal("got a held lock")
		}
		// The holder still holds it.
		if _, ok, _ := b.TryLock(ctx, "job"); ok {
			t.Fatal("a cancelled wait released the holder's lock")
		}
	})
}
