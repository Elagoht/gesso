// Package cachetest holds every cache.Cache to the same contract.
//
//	cachetest.Run(t, func(t *testing.T, c clock.Clock) cache.Cache { return newMyCache(t, c) })
//
// open receives the manual clock the suite moves to test expiry; an
// implementation whose store keeps its own time (Redis) cannot follow it and
// should use RunWithoutClock.
package cachetest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Elagoht/gesso/cache"
	"github.com/Elagoht/gesso/clock"
)

// Run checks the whole contract, expiry included, on a fresh cache per case.
func Run(t *testing.T, open func(t *testing.T, c clock.Clock) cache.Cache) {
	RunWithoutClock(t, func(t *testing.T) cache.Cache { return open(t, clock.System) })

	t.Run("expiry", func(t *testing.T) {
		ctx := context.Background()
		clk := clock.NewManual(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
		c := open(t, clk)
		mustSet(t, c, "short", "v", time.Minute)
		mustSet(t, c, "forever", "v", 0)
		clk.Advance(59 * time.Second)
		if _, ok, _ := c.Get(ctx, "short"); !ok {
			t.Fatal("expired early")
		}
		clk.Advance(time.Second)
		if _, ok, _ := c.Get(ctx, "short"); ok {
			t.Fatal("still there at its ttl")
		}
		clk.Advance(24 * time.Hour * 365)
		if _, ok, _ := c.Get(ctx, "forever"); !ok {
			t.Fatal("a ttl of 0 expired")
		}
	})
}

// RunWithoutClock checks everything but expiry.
func RunWithoutClock(t *testing.T, open func(t *testing.T) cache.Cache) {
	ctx := context.Background()

	t.Run("set then get", func(t *testing.T) {
		c := open(t)
		if _, ok, err := c.Get(ctx, "k"); ok || err != nil {
			t.Fatalf("empty cache: %v %v", ok, err)
		}
		mustSet(t, c, "k", "v1", time.Hour)
		mustSet(t, c, "k", "v2", time.Hour)
		got, ok, err := c.Get(ctx, "k")
		if !ok || err != nil || string(got) != "v2" {
			t.Fatalf("got %q %v %v", got, ok, err)
		}
	})

	t.Run("values are copies", func(t *testing.T) {
		c := open(t)
		v := []byte("abc")
		if err := c.Set(ctx, "k", v, time.Hour); err != nil {
			t.Fatal(err)
		}
		v[0] = 'X'
		got, _, _ := c.Get(ctx, "k")
		got[1] = 'Y'
		again, _, _ := c.Get(ctx, "k")
		if !bytes.Equal(again, []byte("abc")) {
			t.Fatalf("got %q", again)
		}
	})

	t.Run("delete", func(t *testing.T) {
		c := open(t)
		mustSet(t, c, "k", "v", time.Hour)
		if err := c.Delete(ctx, "k"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := c.Get(ctx, "k"); ok {
			t.Fatal("deleted key still there")
		}
		if err := c.Delete(ctx, "k"); err != nil {
			t.Fatalf("deleting a missing key is not an error: %v", err)
		}
	})

	t.Run("get or load", func(t *testing.T) {
		c := open(t)
		calls := 0
		load := func(context.Context) ([]string, error) { calls++; return []string{"a", "b"}, nil }
		for range 3 {
			v, err := cache.GetOrLoad(ctx, c, "list", time.Hour, load)
			if err != nil || len(v) != 2 {
				t.Fatalf("got %v %v", v, err)
			}
		}
		if calls != 1 {
			t.Fatalf("loaded %d times", calls)
		}
	})
}

func mustSet(t *testing.T, c cache.Cache, key, value string, ttl time.Duration) {
	t.Helper()
	if err := c.Set(context.Background(), key, []byte(value), ttl); err != nil {
		t.Fatal(err)
	}
}
