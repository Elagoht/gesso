package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Elagoht/gesso/cache"
	"github.com/Elagoht/gesso/cache/cachetest"
	"github.com/Elagoht/gesso/clock"
)

func TestMemory(t *testing.T) {
	cachetest.Run(t, func(_ *testing.T, c clock.Clock) cache.Cache { return cache.NewMemory(0, c) })
}

func TestMemoryEvictsLeastRecentlyUsed(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemory(2, nil)
	_ = c.Set(ctx, "a", []byte("1"), 0)
	_ = c.Set(ctx, "b", []byte("2"), 0)
	_, _, _ = c.Get(ctx, "a") // a is now the most recent
	_ = c.Set(ctx, "c", []byte("3"), 0)
	if _, ok, _ := c.Get(ctx, "b"); ok {
		t.Fatal("b was the least recently used")
	}
	if _, ok, _ := c.Get(ctx, "a"); !ok || c.Len() != 2 {
		t.Fatalf("a evicted, len %d", c.Len())
	}
}

type brokenCache struct{}

func (brokenCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errors.New("down")
}
func (brokenCache) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("down")
}
func (brokenCache) Delete(context.Context, string) error { return errors.New("down") }

func TestGetOrLoadSurvivesABrokenCache(t *testing.T) {
	v, err := cache.GetOrLoad(context.Background(), brokenCache{}, "k", time.Minute,
		func(context.Context) (int, error) { return 7, nil })
	if v != 7 || err != nil {
		t.Fatalf("got %v %v", v, err)
	}
	errLoad := errors.New("load failed")
	c := cache.NewMemory(0, nil)
	if _, err := cache.GetOrLoad(context.Background(), c, "k", time.Minute,
		func(context.Context) (int, error) { return 0, errLoad }); !errors.Is(err, errLoad) || c.Len() != 0 {
		t.Fatalf("a failed load is returned and not cached: %v, len %d", err, c.Len())
	}
}
