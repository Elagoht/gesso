package integration

import (
	"context"
	"testing"

	"github.com/Elagoht/gesso/idempotency"
	"github.com/Elagoht/gesso/idempotency/idempotencytest"
)

func TestIdempotencySQL(t *testing.T) {
	for name, q := range queueDBs {
		t.Run(name, func(t *testing.T) {
			d := q.open(t)
			if err := idempotency.Migrate(context.Background(), d, q.dialect); err != nil {
				t.Fatal(err)
			}
			idempotencytest.Run(t, func(*testing.T) idempotency.Store { return idempotency.NewSQL(d, q.dialect) })
		})
	}
}
