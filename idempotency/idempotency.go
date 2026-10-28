// Package idempotency lets a client retry a request that changes something —
// a payment, an order — without doing it twice. The client sends a key of its
// choosing in Idempotency-Key; the first request with it runs, and its answer
// is kept; a retry with the same key gets the kept answer, marked
// Idempotent-Replayed: true, and the handler does not run again.
//
//	keys := idempotency.New(idempotency.Options{Store: idempotency.NewSQL(pool, migrate.Postgres)})
//	r.Group("/orders", func(g *gesso.Router) {
//		g.Use(keys.Middleware())
//		…
//	})
//
// A retry while the first is still running is answered 409; the same key with
// another request (method, path or body) 422. An answer of 500 or more is not
// kept, so the client may try again. Keys are kept apart per client — by the
// request's credential, or its address when it has none — so one client cannot
// read another's answer by guessing its key. Requests without the header pass
// through, unless Required.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/clock"
)

// State is what Begin found under a key.
type State int

const (
	// Claimed: nobody had the key, and now the caller does: it runs the
	// request and Finishes it.
	Claimed State = iota
	// Running: another request with the key is running.
	Running
	// Done: the request ran; Begin returns its answer.
	Done
	// Mismatch: the key was used for another request.
	Mismatch
)

// Answer is a kept answer.
type Answer struct {
	Status      int
	ContentType string
	Location    string
	Body        []byte
}

// Store keeps keys and answers. Every implementation passes
// idempotencytest.Run.
type Store interface {
	// Begin claims id for a request with fingerprint at now: Claimed when it was
	// free — or held by a request that stopped answering, its lock past —
	// and the claim is the caller's until lock; else what is there.
	Begin(ctx context.Context, id, fingerprint string, now time.Time, lock, keep time.Duration) (State, Answer, error)
	// Finish keeps a for the claim's id until its keep runs out.
	Finish(ctx context.Context, id string, a Answer) error
	// Abandon frees a claim whose request failed, for a retry to run.
	Abandon(ctx context.Context, id string) error
}

// Options configure the middleware.
type Options struct {
	// Store keeps the keys: NewMemory for one process, NewSQL across replicas.
	Store Store
	// Required refuses a request without the header with 400.
	Required bool
	// Keep is how long an answer is kept for retries. Default 24h.
	Keep time.Duration
	// Lock is how long a running request holds its key; a request that has
	// not finished by then is taken to have died. Default 1m.
	Lock time.Duration
	// MaxBody bounds the request body read to fingerprint it, and the answer
	// kept. Default 1 MiB.
	MaxBody int64
	// Scope names the client keys belong to. Default: the request's
	// Authorization header, else its address.
	Scope  func(*http.Request) string
	Clock  clock.Clock
	Logger *slog.Logger
}

// Keys is the middleware's state.
type Keys struct{ opts Options }

// New makes the middleware's state. It panics without a Store.
func New(opts Options) *Keys {
	if opts.Store == nil {
		panic("idempotency: Options.Store is nil")
	}
	if opts.Keep <= 0 {
		opts.Keep = 24 * time.Hour
	}
	if opts.Lock <= 0 {
		opts.Lock = time.Minute
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = 1 << 20
	}
	if opts.Scope == nil {
		opts.Scope = func(r *http.Request) string {
			if a := r.Header.Get("Authorization"); a != "" {
				return "auth:" + a
			}
			return "addr:" + r.RemoteAddr
		}
	}
	if opts.Clock == nil {
		opts.Clock = clock.System
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Keys{opts: opts}
}

var (
	// ErrKeyMissing is a request without Idempotency-Key where one is Required.
	ErrKeyMissing = gesso.BadRequest("idempotency_key_missing", "send an Idempotency-Key header")
	// ErrKeyInvalid is a key that is empty or longer than 255 characters.
	ErrKeyInvalid = gesso.BadRequest("idempotency_key_invalid", "the Idempotency-Key must be 1 to 255 characters")
	// ErrInProgress is a retry while the first request is still running.
	ErrInProgress = gesso.Conflict("idempotency_in_progress", "a request with this Idempotency-Key is still running")
	// ErrKeyReused is a key sent again with another request.
	ErrKeyReused = gesso.Unprocessable("idempotency_key_reused", "this Idempotency-Key was used for another request")
)

// Middleware applies the keys to requests that change something: every method
// but GET, HEAD and OPTIONS.
func (k *Keys) Middleware() gesso.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				if k.opts.Required {
					gesso.WriteProblem(w, r, ErrKeyMissing)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > 255 {
				gesso.WriteProblem(w, r, ErrKeyInvalid)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, k.opts.MaxBody))
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					gesso.WriteProblem(w, r, gesso.ErrBodyTooLarge)
					return
				}
				gesso.WriteProblem(w, r, gesso.ErrInvalidJSON)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			id := hash(k.opts.Scope(r), "\x00", key)
			fingerprint := hash(r.Method, "\x00", r.URL.RequestURI(), "\x00", string(body))
			ctx := r.Context()
			state, kept, err := k.opts.Store.Begin(ctx, id, fingerprint, k.opts.Clock.Now(), k.opts.Lock, k.opts.Keep)
			if err != nil {
				k.opts.Logger.ErrorContext(ctx, "idempotency: the store failed", "error", err)
				gesso.WriteProblem(w, r, gesso.ErrUnavailable)
				return
			}
			switch state {
			case Running:
				gesso.WriteProblem(w, r, ErrInProgress)
				return
			case Mismatch:
				gesso.WriteProblem(w, r, ErrKeyReused)
				return
			case Done:
				replay(w, kept)
				return
			}
			rec := &recorder{ResponseWriter: w, max: k.opts.MaxBody}
			finished := false
			defer func() {
				if !finished {
					// The handler panicked: free the key for a retry.
					_ = k.opts.Store.Abandon(context.WithoutCancel(ctx), id)
				}
			}()
			next.ServeHTTP(rec, r)
			finished = true
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			sctx := context.WithoutCancel(ctx)
			if status >= 500 || rec.over {
				if rec.over {
					k.opts.Logger.WarnContext(ctx, "idempotency: the answer is too large to keep; a retry runs again", "max", k.opts.MaxBody)
				}
				if err := k.opts.Store.Abandon(sctx, id); err != nil {
					k.opts.Logger.ErrorContext(ctx, "idempotency: abandon", "error", err)
				}
				return
			}
			a := Answer{Status: status, ContentType: rec.Header().Get("Content-Type"), Location: rec.Header().Get("Location"), Body: rec.body.Bytes()}
			if err := k.opts.Store.Finish(sctx, id, a); err != nil {
				k.opts.Logger.ErrorContext(ctx, "idempotency: finish", "error", err)
			}
		})
	}
}

func replay(w http.ResponseWriter, a Answer) {
	h := w.Header()
	if a.ContentType != "" {
		h.Set("Content-Type", a.ContentType)
	}
	if a.Location != "" {
		h.Set("Location", a.Location)
	}
	h.Set("Idempotent-Replayed", "true")
	h.Set("Content-Length", strconv.Itoa(len(a.Body)))
	w.WriteHeader(a.Status)
	_, _ = w.Write(a.Body)
}

func hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = io.WriteString(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// recorder passes the answer on and keeps a copy, up to max bytes.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	max    int64
	over   bool
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if !r.over {
		if int64(r.body.Len()+len(b)) > r.max {
			r.over = true
			r.body.Reset()
		} else {
			r.body.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
