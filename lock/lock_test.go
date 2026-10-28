package lock_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Elagoht/gesso/lock"
	"github.com/Elagoht/gesso/lock/locktest"
)

func TestMemory(t *testing.T) {
	locktest.Run(t, func() func(*testing.T) lock.Locker {
		shared := map[*testing.T]*lock.Memory{}
		return func(t *testing.T) lock.Locker {
			if m, ok := shared[t]; ok {
				return m
			}
			m := lock.NewMemory()
			shared[t] = m
			return m
		}
	}())
}

// A name is forgotten once nobody holds or waits for it.
func TestMemoryForgetsNames(t *testing.T) {
	ctx := context.Background()
	m := lock.NewMemory()
	for i := range 1000 {
		u, ok, _ := m.TryLock(ctx, fmt.Sprint("user-", i))
		if !ok {
			t.Fatal("free")
		}
		_ = u(ctx)
	}
	u, _ := m.Lock(ctx, "held")
	if _, ok, _ := m.TryLock(ctx, "held"); ok {
		t.Fatal("taken twice")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Lock(cctx, "held"); err == nil {
		t.Fatal("a cancelled wait took it")
	}
	_ = u(ctx)
	if n := lock.Held(m); n != 0 {
		t.Fatalf("%d names kept after every lock was let go", n)
	}
}
