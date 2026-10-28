package lock_test

import (
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
