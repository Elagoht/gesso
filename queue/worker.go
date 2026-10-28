package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
)

// Kind is a kind of job and the type of its payload, declared once:
//
//	var SendWelcome = queue.Kind[Welcome]{Name: "send_welcome"}
//
//	// in the service, inside the transaction that creates the user:
//	_, err := SendWelcome.Enqueue(ctx, s.queue, tx, Welcome{UserID: id})
//
//	// in the worker's wiring:
//	queue.Handle(w, SendWelcome, mailer.sendWelcome)
type Kind[T any] struct {
	Name string
	// MaxAttempts before a job is dead. Zero: 5.
	MaxAttempts int
}

// Enqueue adds a job of this kind through d, its payload as JSON.
func (k Kind[T]) Enqueue(ctx context.Context, q Queue, d db.DB, payload T) (int64, error) {
	return k.EnqueueAt(ctx, q, d, payload, time.Time{})
}

// EnqueueAt adds a job that runs no earlier than at.
func (k Kind[T]) EnqueueAt(ctx context.Context, q Queue, d db.DB, payload T, at time.Time) (int64, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("queue: %s: %w", k.Name, err)
	}
	return q.Enqueue(ctx, d, NewJob{Kind: k.Name, Payload: data, RunAt: at, MaxAttempts: k.MaxAttempts})
}

// ErrBadPayload is a payload that does not decode: the job is buried at once,
// since no retry can mend it.
var ErrBadPayload = errors.New("queue: the payload does not decode")

// Handle runs fn for every job of kind k that w claims.
func Handle[T any](w *Worker, k Kind[T], fn func(ctx context.Context, payload T) error) {
	w.handlers[k.Name] = func(ctx context.Context, j Job) error {
		var p T
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return fmt.Errorf("%w: %v", ErrBadPayload, err)
		}
		return fn(ctx, p)
	}
	w.kinds = append(w.kinds, k.Name)
}

// WorkerOptions tune a worker. The zero value is sensible.
type WorkerOptions struct {
	// Interval between polls when there was nothing to do. Default 1s.
	Interval time.Duration
	// Lease a job is claimed for, from the moment its batch is claimed; a
	// handler's ctx ends with it, and a job of the batch whose lease has run
	// out before its turn is left for the next claim. Default 1m.
	Lease time.Duration
	// Batch is how many jobs of a kind one poll claims. Default 10.
	Batch int
	// Backoff is the wait after a job's first failure, doubled each attempt up to
	// MaxBackoff. Defaults 10s and 1h.
	Backoff, MaxBackoff time.Duration
	Clock               clock.Clock
	Logger              *slog.Logger
}

// Worker claims and runs jobs. It is a gesso module with no routes: pass it to
// gesso.New and the app starts and stops it. It runs only where the app's Start
// is called — gesso as its own process, not mounted under collage.
type Worker struct {
	q        Queue
	opt      WorkerOptions
	handlers map[string]func(context.Context, Job) error
	kinds    []string

	mu      sync.Mutex
	cancel  context.CancelFunc
	running sync.WaitGroup
}

// NewWorker works q.
func NewWorker(q Queue, opt WorkerOptions) *Worker {
	if opt.Interval <= 0 {
		opt.Interval = time.Second
	}
	if opt.Lease <= 0 {
		opt.Lease = time.Minute
	}
	if opt.Batch <= 0 {
		opt.Batch = 10
	}
	if opt.Backoff <= 0 {
		opt.Backoff = 10 * time.Second
	}
	if opt.MaxBackoff <= 0 {
		opt.MaxBackoff = time.Hour
	}
	if opt.Clock == nil {
		opt.Clock = clock.System
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	return &Worker{q: q, opt: opt, handlers: map[string]func(context.Context, Job) error{}}
}

// Routes registers nothing: a worker serves no requests.
func (w *Worker) Routes(*gesso.Router) {}

// RunOnce claims one batch of every kind and runs it, and reports how many jobs
// it ran. The loop calls it; a test calls it directly.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	ran := 0
	var errs []error
	for _, kind := range slices.Sorted(slices.Values(w.kinds)) {
		// The lease runs from the claim, for the whole batch: a job run late in
		// it has only what is left, and one whose lease is gone is another
		// worker's to take — running it here too would run it twice.
		deadline := time.Now().Add(w.opt.Lease)
		jobs, err := w.q.Claim(ctx, kind, w.opt.Batch, w.opt.Lease)
		if err != nil {
			errs = append(errs, fmt.Errorf("queue: claim %s: %w", kind, err))
			continue
		}
		for i, j := range jobs {
			// Leave a margin: a job started on the lease's last instant could
			// only fail on its deadline.
			if time.Until(deadline) <= w.opt.Lease/10 {
				for _, rest := range jobs[i:] {
					// Never run, so its attempt was not spent: back to the
					// queue, ready now.
					if err := w.q.Release(ctx, rest); err != nil && !errors.Is(err, ErrLeaseLost) {
						errs = append(errs, fmt.Errorf("queue: release %s %d: %w", kind, rest.ID, err))
					}
				}
				break
			}
			w.run(ctx, j, deadline)
			ran++
		}
	}
	return ran, errors.Join(errs...)
}

func (w *Worker) run(ctx context.Context, j Job, deadline time.Time) {
	jctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	err := w.call(jctx, j)
	if err == nil {
		if cerr := w.q.Complete(ctx, j); cerr != nil {
			w.opt.Logger.Warn("queue: complete", "kind", j.Kind, "id", j.ID, "error", cerr)
		}
		return
	}
	bad := errors.Is(err, ErrBadPayload)
	if bad || j.Last() {
		w.opt.Logger.Error("queue: job dead", "kind", j.Kind, "id", j.ID, "attempt", j.Attempt, "error", err)
	}
	var ferr error
	if bad {
		ferr = w.q.Bury(ctx, j, err.Error()) // no retry can mend a payload
	} else {
		ferr = w.q.Fail(ctx, j, err.Error(), w.opt.Clock.Now().Add(w.backoff(j.Attempt)))
	}
	if ferr != nil {
		w.opt.Logger.Warn("queue: settle", "kind", j.Kind, "id", j.ID, "error", ferr)
	}
}

func (w *Worker) call(ctx context.Context, j Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("queue: handler panicked: %v", p)
		}
	}()
	return w.handlers[j.Kind](ctx, j)
}

func (w *Worker) backoff(attempt int) time.Duration {
	d := w.opt.Backoff
	for i := 1; i < attempt && d < w.opt.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, w.opt.MaxBackoff)
}

// Start begins polling in the background.
func (w *Worker) Start(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return errors.New("queue: worker already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.running.Go(func() { w.loop(ctx) })
	return nil
}

func (w *Worker) loop(ctx context.Context) {
	failures := 0
	for {
		ran, err := w.RunOnce(ctx)
		wait := w.opt.Interval
		if err != nil {
			// The database is down, or worse: back off instead of spinning.
			failures++
			wait = min(w.opt.Interval<<min(failures, 6), w.opt.MaxBackoff)
			w.opt.Logger.Error("queue: poll", "error", err, "retry_in", wait)
		} else {
			failures = 0
			if ran > 0 {
				wait = 0 // more may be waiting
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Stop ends polling and waits for the jobs running now, until ctx ends.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() { w.running.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("queue: stop: jobs still running: %w", ctx.Err())
	}
}
