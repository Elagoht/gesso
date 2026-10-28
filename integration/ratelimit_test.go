package integration

import (
	"context"
	"testing"
	"time"

	"github.com/Elagoht/gesso/ratelimit"
	"github.com/Elagoht/gesso/ratelimit/ratelimittest"
)

// One table, shared as replicas share it: the suite's "at once" test takes
// through two stores on one database.
func TestRateLimitSQL(t *testing.T) {
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := q.open(t)
			ctx := context.Background()
			if err := ratelimit.Migrate(ctx, d, q.dialect); err != nil {
				t.Fatal(err)
			}
			ratelimittest.Run(t, func(*testing.T) ratelimit.Store { return ratelimit.NewSQL(d, q.dialect) })

			s := ratelimit.NewSQL(d, q.dialect)
			now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
			if _, _, err := s.Take(ctx, "purged", now, time.Minute, 1); err != nil {
				t.Fatal(err)
			}
			n, err := s.Purge(ctx, now.Add(2*time.Hour))
			if err != nil || n == 0 {
				t.Fatalf("purge: %d %v", n, err)
			}
			var left int
			if err := d.QueryRow("SELECT COUNT(*) FROM gesso_ratelimit").Scan(&left); err != nil || left != 0 {
				t.Fatalf("two hours on, every bucket is full: %d rows left, %v", left, err)
			}
		})
	}
}
