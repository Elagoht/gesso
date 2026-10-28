package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/gessotest"
	"github.com/Elagoht/gesso/queue"
	"github.com/Elagoht/gesso/queue/queuetest"
)

func TestPostgresQueue(t *testing.T) {
	d := pgDB(t)
	if err := queue.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	queuetest.Run(t, func(t *testing.T, c clock.Clock) (queue.Queue, db.DB) {
		tx := gessotest.Tx(t, d)
		return queue.NewPostgres(tx, c), tx
	})
}

// The outbox's point: a job exists if and only if the transaction that enqueued
// it committed.
func TestPostgresOutboxIsAtomic(t *testing.T) {
	d := pgDB(t)
	ctx := context.Background()
	if err := queue.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec("CREATE TABLE users (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	q := queue.NewPostgres(d, nil)
	signup := func(name string, fail bool) error {
		return db.InTx(ctx, d, func(tx db.DB) error {
			var id int64
			if err := tx.QueryRowContext(ctx, "INSERT INTO users (name) VALUES ($1) RETURNING id", name).Scan(&id); err != nil {
				return err
			}
			if _, err := q.Enqueue(ctx, tx, queue.NewJob{Kind: "welcome"}); err != nil {
				return err
			}
			if fail {
				return errors.New("a later step failed")
			}
			return nil
		})
	}
	if err := signup("ada", true); err == nil {
		t.Fatal("want the error")
	}
	if err := signup("bob", false); err != nil {
		t.Fatal(err)
	}
	var users, jobs int
	_ = d.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
	_ = d.QueryRow("SELECT COUNT(*) FROM gesso_jobs").Scan(&jobs)
	if users != 1 || jobs != 1 {
		t.Fatalf("users %d, jobs %d: the rolled-back signup left its job, or the committed one lost it", users, jobs)
	}
}

// Claimers racing over the same jobs take each exactly once.
func TestPostgresQueueClaimsEachJobOnce(t *testing.T) {
	d := pgDB(t)
	ctx := context.Background()
	if err := queue.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	q := queue.NewPostgres(d, nil)
	const n = 200
	for range n {
		if _, err := q.Enqueue(ctx, d, queue.NewJob{Kind: "k"}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				jobs, err := q.Claim(ctx, "k", 7, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if len(jobs) == 0 {
					return
				}
				mu.Lock()
				for _, j := range jobs {
					seen[j.ID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d distinct jobs of %d", len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("job %d claimed %d times", id, c)
		}
	}
}
