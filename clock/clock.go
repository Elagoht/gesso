// Package clock is time as a dependency: code that asks a Clock for the time,
// instead of calling time.Now, can be tested at any moment it likes.
package clock

import (
	"sync"
	"time"
)

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

type system struct{}

func (system) Now() time.Time { return time.Now() }

// System is the real clock.
var System Clock = system{}

// Manual is a clock that moves only when told to. It is safe for concurrent use.
type Manual struct {
	mu  sync.Mutex
	now time.Time
}

// NewManual is a manual clock standing at t.
func NewManual(t time.Time) *Manual { return &Manual{now: t} }

// Now is the time the clock stands at.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Advance moves the clock forward by d.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

// Set moves the clock to t.
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t
}
