package queue_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/events"
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

// A batch shares one lease, from its claim: the jobs the first leaves no time
// for are handed back, unrun and with their attempt given back — not run late
// beside the worker that may take them, nor dead for never having run.
func TestWorkerLeaseRunsFromTheClaim(t *testing.T) {
	ctx := context.Background()
	q := queue.NewMemory(nil)
	const lease = time.Second
	w := queue.NewWorker(q, queue.WorkerOptions{Lease: lease, Batch: 3, Logger: slog.New(slog.DiscardHandler)})
	kind := queue.Kind[int]{Name: "slow", MaxAttempts: 1}
	var ran []int
	queue.Handle(w, kind, func(ctx context.Context, n int) error {
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > lease {
			t.Errorf("the job's deadline is past the batch's lease: %v", time.Until(dl))
		}
		ran = append(ran, n)
		time.Sleep(time.Until(dl) - 50*time.Millisecond) // most of what is left
		return nil
	})
	for i := range 3 {
		if _, err := kind.Enqueue(ctx, q, nil, i); err != nil {
			t.Fatal(err)
		}
	}
	for round := range 3 {
		n, err := w.RunOnce(ctx)
		if err != nil || n != 1 {
			t.Fatalf("round %d ran %d: %v", round, n, err)
		}
	}
	if len(ran) != 3 {
		t.Fatalf("ran %v", ran)
	}
	if dead, _ := q.Dead(ctx, "slow", 10); len(dead) != 0 {
		t.Fatalf("dead though they ran in time: %+v", dead)
	}
}

// A schedule fires once per tick across replicas, on the clock's multiples,
// and a worker that slept through ticks fires once, for the latest.
func TestSchedules(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(time.Date(2026, 10, 29, 9, 59, 30, 0, time.UTC))
	q := queue.NewMemory(clk)
	report := queue.Kind[string]{Name: "report"}
	var got []string
	newWorker := func() *queue.Worker {
		w := queue.NewWorker(q, queue.WorkerOptions{Clock: clk, Logger: slog.New(slog.DiscardHandler),
			Location: time.FixedZone("TRT", 3*3600)})
		queue.Handle(w, report, func(_ context.Context, p string) error {
			got = append(got, p+"@"+clk.Now().Format("15:04"))
			return nil
		})
		queue.Every(w, "hourly", report, time.Hour, "hourly")
		queue.Cron(w, "nightly", report, "0 3 * * *", "nightly") // 03:00 in TRT is 00:00 UTC
		return w
	}
	a, b := newWorker(), newWorker() // two replicas
	run := func() {
		for _, w := range []*queue.Worker{a, b} {
			if _, err := w.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	run() // the first poll only learns the next ticks
	clk.Set(time.Date(2026, 10, 29, 10, 0, 5, 0, time.UTC))
	run()
	if strings.Join(got, ",") != "hourly@10:00" {
		t.Fatalf("on the hour, once across two replicas: %v", got)
	}
	got = nil
	clk.Set(time.Date(2026, 10, 30, 0, 0, 1, 0, time.UTC)) // slept through 14 hourly ticks
	run()
	if strings.Join(got, ",") != "hourly@00:00,nightly@00:00" {
		t.Fatalf("after a long sleep: %v", got)
	}
}

func TestScheduleMisuse(t *testing.T) {
	w := queue.NewWorker(queue.NewMemory(nil), queue.WorkerOptions{})
	k := queue.Kind[int]{Name: "k"}
	for name, define := range map[string]func(){
		"bad cron":   func() { queue.Cron(w, "x", k, "* * *", 0) },
		"never":      func() { queue.Cron(w, "y", k, "0 0 30 2 *", 0) },
		"zero every": func() { queue.Every(w, "z", k, 0, 0) },
		"twice": func() {
			queue.Every(w, "dup", k, time.Minute, 0)
			queue.Every(w, "dup", k, time.Minute, 0)
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			define()
		}()
	}
}

type deletedEvent struct {
	Slug string `json:"slug"`
}

var noteDeleted = events.Topic[deletedEvent]{Name: "notes.deleted"}

// An event Published through the queue reaches another process's bus; a
// subscriber that fails gets it again.
func TestEventsThroughTheQueue(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	q := queue.NewMemory(clk)
	if _, err := queue.Publish(ctx, q, nil, noteDeleted, deletedEvent{Slug: "jam"}); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus() // the other process's
	var got []string
	fail := true
	noteDeleted.Subscribe(bus, func(_ context.Context, e deletedEvent) error {
		got = append(got, e.Slug)
		if fail {
			fail = false
			return errors.New("search index down")
		}
		return nil
	})
	w := queue.NewWorker(q, queue.WorkerOptions{Clock: clk, Backoff: time.Second, Logger: slog.New(slog.DiscardHandler)})
	queue.Deliver(w, bus, noteDeleted)
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Second)
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "jam,jam" {
		t.Fatalf("delivered %v: once, failed, then again", got)
	}
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("delivered after it succeeded")
	}
}
