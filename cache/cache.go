// Package cache keeps computed values for a while. The contract is bytes, so any
// store can hold them (gesso-redis is one); GetOrLoad adds the types back, as
// JSON. Memory is here, and every implementation passes cachetest.Run.
package cache

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Elagoht/gesso/clock"
)

// Cache keeps values under keys for a time.
type Cache interface {
	// Get returns the value under key, or false when there is none or it has
	// expired.
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// Set stores value under key for ttl; a ttl of 0 or less keeps it until it
	// is deleted or evicted.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Delete removes key. A missing key is not an error.
	Delete(ctx context.Context, key string) error
}

// GetOrLoad returns the value under key, or calls load, caches its result for
// ttl and returns it. A cache that fails is passed over: the value is loaded, and
// the cache's error is not the caller's. load's error is returned and nothing is
// cached.
func GetOrLoad[T any](ctx context.Context, c Cache, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	if data, ok, err := c.Get(ctx, key); err == nil && ok {
		var v T
		if json.Unmarshal(data, &v) == nil {
			return v, nil
		}
	}
	v, err := load(ctx)
	if err != nil {
		return v, err
	}
	if data, err := json.Marshal(v); err == nil {
		_ = c.Set(ctx, key, data, ttl)
	}
	return v, nil
}

// Memory is an in-process cache that evicts the least recently used entry beyond
// its capacity. It is the reference implementation and the fake for tests.
type Memory struct {
	mu      sync.Mutex
	max     int
	clock   clock.Clock
	entries map[string]*list.Element
	order   *list.List // front: most recently used
}

type entry struct {
	key     string
	value   []byte
	expires time.Time // zero: never
}

// NewMemory holds at most maxEntries (0 for no limit); c tells the time for
// expiry (nil for the system clock).
func NewMemory(maxEntries int, c clock.Clock) *Memory {
	if c == nil {
		c = clock.System
	}
	return &Memory{max: maxEntries, clock: c, entries: map[string]*list.Element{}, order: list.New()}
}

// Get returns a copy of the value under key.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.entries[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*entry)
	if !e.expires.IsZero() && !m.clock.Now().Before(e.expires) {
		m.remove(el)
		return nil, false, nil
	}
	m.order.MoveToFront(el)
	return append([]byte(nil), e.value...), true, nil
}

// Set stores a copy of value.
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := &entry{key: key, value: append([]byte(nil), value...)}
	if ttl > 0 {
		e.expires = m.clock.Now().Add(ttl)
	}
	if el, ok := m.entries[key]; ok {
		el.Value = e
		m.order.MoveToFront(el)
		return nil
	}
	m.entries[key] = m.order.PushFront(e)
	if m.max > 0 && m.order.Len() > m.max {
		m.remove(m.order.Back())
	}
	return nil
}

// Delete removes key.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		m.remove(el)
	}
	return nil
}

// Len is how many entries are held, expired ones not yet noticed included.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.order.Len()
}

func (m *Memory) remove(el *list.Element) {
	m.order.Remove(el)
	delete(m.entries, el.Value.(*entry).key)
}
