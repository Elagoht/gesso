package integration

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso/cache"
	"github.com/Elagoht/gesso/cache/cachetest"
	"github.com/Elagoht/gesso/cache/redis"
)

// A real Redis: GESSO_REDIS_ADDR (and GESSO_REDIS_PASSWORD), which CI sets;
// without it the Redis tests skip, loudly.
func redisOptions(t *testing.T) redis.Options {
	t.Helper()
	addr := os.Getenv("GESSO_REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS UNVERIFIED — GESSO_REDIS_ADDR is not set")
	}
	return redis.Options{Addr: addr, Password: os.Getenv("GESSO_REDIS_PASSWORD")}
}

func TestRedisCache(t *testing.T) {
	opts := redisOptions(t)
	n := 0
	// Redis times keys on its own clock, which a test's cannot move: the
	// suite without expiry, and expiry in real time below.
	cachetest.RunWithoutClock(t, func(t *testing.T) cache.Cache {
		n++
		o := opts
		o.Prefix = "gesso-test-" + time.Now().Format("150405.000000") + "-" + string(rune('a'+n%26)) + ":"
		c, err := redis.New(o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	})
}

func TestRedisExpiry(t *testing.T) {
	c, err := redis.New(redisOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	_ = c.Set(ctx, "gesso-short", []byte("v"), 200*time.Millisecond)
	_ = c.Set(ctx, "gesso-forever", []byte("v"), 0)
	if _, ok, _ := c.Get(ctx, "gesso-short"); !ok {
		t.Fatal("expired early")
	}
	time.Sleep(300 * time.Millisecond)
	if _, ok, _ := c.Get(ctx, "gesso-short"); ok {
		t.Fatal("still there past its ttl")
	}
	if _, ok, _ := c.Get(ctx, "gesso-forever"); !ok {
		t.Fatal("a ttl of 0 expired")
	}
	_ = c.Delete(ctx, "gesso-forever")
}

func TestRedisFailures(t *testing.T) {
	opts := redisOptions(t)
	bad := opts
	bad.Password = "wrong"
	if _, err := redis.New(bad); err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("a wrong password: %v", err)
	}
	c, err := redis.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 1<<20)
	if err := c.Set(ctx, "gesso-big", []byte(big), time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := c.Get(ctx, "gesso-big"); err != nil || !ok || string(got) != big {
		t.Fatalf("a 1 MiB value: %d %v %v", len(got), ok, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := c.Get(cctx, "gesso-big"); err == nil {
		t.Fatal("a cancelled ctx did not stop the command")
	}
	// After a failed command the next still works: the broken connection was
	// not kept.
	if _, ok, err := c.Get(ctx, "gesso-big"); err != nil || !ok {
		t.Fatalf("after a failure: %v %v", ok, err)
	}
	var re redis.Error
	if _, _, err := c.Get(ctx, ""); err != nil && !errors.As(err, &re) {
		t.Fatalf("%v", err)
	}
}
