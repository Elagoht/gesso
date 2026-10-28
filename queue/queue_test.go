package queue_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/queue"
	"github.com/Elagoht/gesso/queue/queuetest"
)

func TestMemory(t *testing.T) {
	queuetest.Run(t, func(_ *testing.T, c clock.Clock) (queue.Queue, db.DB) { return queue.NewMemory(c), nil })
}

type welcome struct {
	UserID int64 `json:"userId"`
}

var sendWelcome = queue.Kind[welcome]{Name: "send_welcome", MaxAttempts: 3}

func newWorker(q queue.Queue, clk clock.Clock, logs *bytes.Buffer) *queue.Worker {
	return queue.NewWorker(q, queue.WorkerOptions{
		Clock: clk, Lease: time.Minute, Backoff: 10 * time.Second, MaxBackoff: time.Minute,
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
	})
}

func TestWorker(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	q := queue.NewMemory(clk)
	var logs bytes.Buffer
	w := newWorker(q, clk, &logs)
	var got []int64
	fails := 0
	queue.Handle(w, sendWelcome, func(ctx context.Context, p welcome) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("a handler's ctx ends with its lease")
		}
		switch p.UserID {
		case 2:
			fails++
			return errors.New("smtp down")
		case 3:
			panic("handler bug")
		}
		got = append(got, p.UserID)
		return nil
	})
	for id := range int64(3) {
		if _, err := sendWelcome.Enqueue(ctx, q, nil, welcome{UserID: id + 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.Enqueue(ctx, nil, queue.NewJob{Kind: sendWelcome.Name, Payload: []byte("not json")}); err != nil {
		t.Fatal(err)
	}

	if n, err := w.RunOnce(ctx); n != 4 || err != nil {
		t.Fatalf("ran %d, %v", n, err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("got %v", got)
	}
	dead, _ := q.Dead(ctx, sendWelcome.Name, 10)
	if len(dead) != 1 || !bytes.Contains([]byte(dead[0].LastError), []byte("does not decode")) || dead[0].Attempt != 1 {
		t.Fatalf("a bad payload is dead at once: %+v", dead)
	}

	// Backoff: 10s after the first failure, 20s after the second.
	clk.Advance(9 * time.Second)
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("retried before its backoff")
	}
	clk.Advance(time.Second)
	if n, _ := w.RunOnce(ctx); n != 2 {
		t.Fatalf("ran %d", n)
	}
	clk.Advance(20 * time.Second)
	if n, _ := w.RunOnce(ctx); n != 2 {
		t.Fatalf("ran %d", n)
	}
	clk.Advance(time.Hour)
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("ran past max attempts")
	}
	dead, _ = q.Dead(ctx, sendWelcome.Name, 10)
	if len(dead) != 3 || fails != 3 || !bytes.Contains(logs.Bytes(), []byte("handler panicked")) {
		t.Fatalf("dead %+v, fails %d, logs %s", dead, fails, logs.String())
	}
}

func TestWorkerStopWaitsForRunningJobs(t *testing.T) {
	q := queue.NewMemory(nil)
	var logs bytes.Buffer
	w := queue.NewWorker(q, queue.WorkerOptions{Interval: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	started := make(chan struct{})
	var finished atomic.Bool
	queue.Handle(w, sendWelcome, func(context.Context, welcome) error {
		close(started)
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)
		return nil
	})
	_, _ = sendWelcome.Enqueue(context.Background(), q, nil, welcome{UserID: 1})
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("Stop returned while a job ran")
	}
}

// failingQueue fails every claim: the worker must back off, not spin.
type failingQueue struct {
	queue.Queue
	claims atomic.Int64
}

func (f *failingQueue) Claim(context.Context, string, int, time.Duration) ([]queue.Job, error) {
	f.claims.Add(1)
	return nil, errors.New("database down")
}

func TestWorkerBacksOffWhenClaimsFail(t *testing.T) {
	f := &failingQueue{Queue: queue.NewMemory(nil)}
	var logs bytes.Buffer
	w := queue.NewWorker(f, queue.WorkerOptions{Interval: 20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	queue.Handle(w, sendWelcome, func(context.Context, welcome) error { return nil })
	_ = w.Start(context.Background())
	time.Sleep(500 * time.Millisecond)
	_ = w.Stop(context.Background())
	// Without backoff: ~25 claims in 500ms. With it: 20, 40, 80, 160ms… about 4.
	if n := f.claims.Load(); n > 6 {
		t.Fatalf("%d claims in 500ms: a hot loop", n)
	}
}

// A batch shares one lease, from its claim: a job whose lease ran out while
// the ones before it ran is left for the next claim, not run late beside the
// worker that has taken it since.
func TestWorkerLeaseRunsFromTheClaim(t *testing.T) {
	ctx := context.Background()
	q := queue.NewMemory(nil)
	w := queue.NewWorker(q, queue.WorkerOptions{Lease: 100 * time.Millisecond, Batch: 3, Logger: slog.New(slog.DiscardHandler)})
	kind := queue.Kind[int]{Name: "slow"}
	var ran []int
	var deadlines []time.Duration
	queue.Handle(w, kind, func(ctx context.Context, n int) error {
		dl, _ := ctx.Deadline()
		deadlines = append(deadlines, time.Until(dl))
		ran = append(ran, n)
		time.Sleep(120 * time.Millisecond)
		return nil
	})
	for i := range 3 {
		if _, err := kind.Enqueue(ctx, q, nil, i); err != nil {
			t.Fatal(err)
		}
	}
	n, err := w.RunOnce(ctx)
	if err != nil || n != 1 || len(ran) != 1 {
		t.Fatalf("ran %d (%v) after the first used up the lease: %v", n, ran, err)
	}
	if deadlines[0] > 100*time.Millisecond {
		t.Fatalf("the job's deadline was past the lease: %v", deadlines[0])
	}
}
