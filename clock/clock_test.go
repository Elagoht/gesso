package clock

import (
	"testing"
	"time"
)

func TestManual(t *testing.T) {
	start := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	m := NewManual(start)
	m.Advance(time.Hour)
	if !m.Now().Equal(start.Add(time.Hour)) {
		t.Fatal(m.Now())
	}
	m.Set(start)
	if !m.Now().Equal(start) {
		t.Fatal(m.Now())
	}
}
