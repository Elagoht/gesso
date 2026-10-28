// Package queuetest holds every queue.Queue to the same contract.
//
//	queuetest.Run(t, func(t *testing.T, c clock.Clock) (queue.Queue, db.DB) {
//	    tx := gessotest.Tx(t, pool)
//	    return queue.NewPostgres(tx, c), tx
//	})
//
// open returns a fresh, empty queue whose time is c's, and the db.DB jobs are
// enqueued through.
package queuetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/queue"
)

// Run checks the contract.
func Run(t *testing.T, open func(t *testing.T, c clock.Clock) (queue.Queue, db.DB)) {
	ctx := context.Background()
	start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	setup := func(t *testing.T) (queue.Queue, db.DB, *clock.Manual) {
		clk := clock.NewManual(start)
		q, d := open(t, clk)
		return q, d, clk
	}
	enqueue := func(t *testing.T, q queue.Queue, d db.DB, j queue.NewJob) int64 {
		t.Helper()
		id, err := q.Enqueue(ctx, d, j)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	claim := func(t *testing.T, q queue.Queue, kind string, n int) []queue.Job {
		t.Helper()
		jobs, err := q.Claim(ctx, kind, n, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return jobs
	}

	t.Run("enqueue, claim, complete", func(t *testing.T) {
		q, d, _ := setup(t)
		id := enqueue(t, q, d, queue.NewJob{Kind: "mail", Payload: []byte(`{"to":"a"}`)})
		jobs := claim(t, q, "mail", 10)
		if len(jobs) != 1 || jobs[0].ID != id || string(jobs[0].Payload) != `{"to":"a"}` || jobs[0].Attempt != 1 || jobs[0].MaxAttempts != 5 {
			t.Fatalf("got %+v", jobs)
		}
		if again := claim(t, q, "mail", 10); len(again) != 0 {
			t.Fatalf("a claimed job is claimed again within its lease: %+v", again)
		}
		if err := q.Complete(ctx, jobs[0]); err != nil {
			t.Fatal(err)
		}
		if err := q.Complete(ctx, jobs[0]); !errors.Is(err, queue.ErrLeaseLost) {
			t.Fatalf("completing twice: %v", err)
		}
	})

	t.Run("kinds, order and batch size", func(t *testing.T) {
		q, d, _ := setup(t)
		a := enqueue(t, q, d, queue.NewJob{Kind: "mail"})
		b := enqueue(t, q, d, queue.NewJob{Kind: "mail"})
		enqueue(t, q, d, queue.NewJob{Kind: "mail"})
		enqueue(t, q, d, queue.NewJob{Kind: "resize"})
		jobs := claim(t, q, "mail", 2)
		if len(jobs) != 2 || jobs[0].ID != a || jobs[1].ID != b {
			t.Fatalf("got %+v", jobs)
		}
		if r := claim(t, q, "resize", 10); len(r) != 1 {
			t.Fatalf("got %+v", r)
		}
	})

	t.Run("run at", func(t *testing.T) {
		q, d, clk := setup(t)
		enqueue(t, q, d, queue.NewJob{Kind: "mail", RunAt: start.Add(time.Hour)})
		if jobs := claim(t, q, "mail", 10); len(jobs) != 0 {
			t.Fatal("claimed before its time")
		}
		clk.Advance(time.Hour)
		if jobs := claim(t, q, "mail", 10); len(jobs) != 1 {
			t.Fatal("not claimed at its time")
		}
	})

	t.Run("a lost lease fences the old claimer out", func(t *testing.T) {
		q, d, clk := setup(t)
		enqueue(t, q, d, queue.NewJob{Kind: "mail"})
		first := claim(t, q, "mail", 1)[0]
		clk.Advance(time.Minute)
		second := claim(t, q, "mail", 1)
		if len(second) != 1 || second[0].Attempt != 2 {
			t.Fatalf("an expired lease is claimed again: %+v", second)
		}
		if err := q.Complete(ctx, first); !errors.Is(err, queue.ErrLeaseLost) {
			t.Fatalf("the old claimer completed another's job: %v", err)
		}
		if err := q.Fail(ctx, first, "late", start); !errors.Is(err, queue.ErrLeaseLost) {
			t.Fatalf("the old claimer failed another's job: %v", err)
		}
		if err := q.Complete(ctx, second[0]); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("fail retries, then the job is dead", func(t *testing.T) {
		q, d, clk := setup(t)
		enqueue(t, q, d, queue.NewJob{Kind: "mail", MaxAttempts: 2})
		j := claim(t, q, "mail", 1)[0]
		if err := q.Fail(ctx, j, "smtp down", start.Add(10*time.Second)); err != nil {
			t.Fatal(err)
		}
		if jobs := claim(t, q, "mail", 1); len(jobs) != 0 {
			t.Fatal("retried before its time")
		}
		clk.Advance(10 * time.Second)
		j = claim(t, q, "mail", 1)[0]
		if j.Attempt != 2 || !j.Last() {
			t.Fatalf("got %+v", j)
		}
		if err := q.Fail(ctx, j, "smtp still down", clk.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Hour)
		if jobs := claim(t, q, "mail", 1); len(jobs) != 0 {
			t.Fatal("a dead job ran again")
		}
		dead, err := q.Dead(ctx, "mail", 10)
		if err != nil || len(dead) != 1 || dead[0].LastError != "smtp still down" || dead[0].Attempt != 2 {
			t.Fatalf("got %+v %v", dead, err)
		}
	})

	t.Run("bury", func(t *testing.T) {
		q, d, clk := setup(t)
		enqueue(t, q, d, queue.NewJob{Kind: "mail", MaxAttempts: 9})
		j := claim(t, q, "mail", 1)[0]
		if err := q.Bury(ctx, j, "bad payload"); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Hour)
		if jobs := claim(t, q, "mail", 1); len(jobs) != 0 {
			t.Fatal("a buried job ran again")
		}
		if dead, _ := q.Dead(ctx, "mail", 10); len(dead) != 1 {
			t.Fatalf("got %+v", dead)
		}
	})
}
