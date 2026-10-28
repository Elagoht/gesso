package gesso

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const defaultMaxBody = 1 << 20

// Config is an App's settings. The zero value serves at "/" with sensible limits.
type Config struct {
	// BasePath is the prefix every route is under: "/api" when collage mounts the
	// app with app.Handle("/api/", …), which passes the full path on; "" when the
	// app runs as its own process at the root.
	BasePath string
	// Addr is where Run listens. Default ":8080".
	Addr string
	// MaxBodyBytes caps a request body. Default 1 MiB.
	MaxBodyBytes int64
	// ShutdownTimeout bounds Run's graceful shutdown. Default 10s.
	ShutdownTimeout time.Duration
	// Logger receives server errors. Default slog.Default().
	Logger *slog.Logger
	// ErrorMappers turn errors gesso does not know into answers, in order: a
	// database's constraint violations (db.Errors), a library's sentinel errors.
	ErrorMappers []ErrorMapper
	// OpenAPI, when set, generates and serves the OpenAPI document.
	OpenAPI *OpenAPIConfig
}

// Module is a feature: it registers its routes. A module that also implements
// Starter or Stopper is started by Start and stopped by Shutdown.
type Module interface {
	Routes(r *Router)
}

// Starter is a module with work to begin before requests: a background job, a
// warm cache. Start must return; long work runs in a goroutine it stops in Stop.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is a module with work to end at shutdown.
type Stopper interface {
	Stop(ctx context.Context) error
}

// App is a gesso application: an http.Handler with a lifecycle.
type App struct {
	cfg      Config
	basePath string
	mux      *http.ServeMux
	errs     errorWriter
	modules  []Module
	errList  []error
	docs     []docRoute
	openapi  []byte

	mu       sync.Mutex
	started  []Module // modules whose Start succeeded, in order
	hooks    []func(context.Context) error
	running  bool
	shutdown bool
	failed   atomic.Bool // Start failed: every request is answered ErrUnavailable
}

// New builds an app from its modules. Every route problem — a bad pattern, two
// routes for one path, a Use after routes, an input gesso cannot decode — is
// reported here, together, not at the first request.
func New(cfg Config, modules ...Module) (*App, error) {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBody
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	a := &App{
		cfg:  cfg,
		mux:  http.NewServeMux(),
		errs: errorWriter{logger: cfg.Logger, mappers: slices.Clone(cfg.ErrorMappers)},
	}
	if bp := strings.TrimSuffix(cfg.BasePath, "/"); bp != "" {
		if !strings.HasPrefix(bp, "/") {
			return nil, fmt.Errorf("gesso: BasePath %q must begin with \"/\"", cfg.BasePath)
		}
		a.basePath = bp
	}
	for _, m := range modules {
		if m == nil {
			a.fail(errors.New("gesso: nil module"))
			continue
		}
		a.modules = append(a.modules, m)
		m.Routes(&Router{app: a})
	}
	if len(a.errList) == 0 {
		if err := a.buildOpenAPI(); err != nil {
			a.fail(fmt.Errorf("gesso: openapi: %w", err))
		}
	}
	if err := errors.Join(a.errList...); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *App) fail(err error) { a.errList = append(a.errList, err) }

func (a *App) register(pattern string, h http.Handler) {
	defer func() {
		// ServeMux panics on a conflicting or malformed pattern; New reports it.
		if p := recover(); p != nil {
			a.fail(fmt.Errorf("gesso: %v", p))
		}
	}()
	a.mux.Handle(pattern, h)
}

// ServeHTTP answers a request. The router's own 404 and 405 are problem+json like
// every other error, and a panic is a logged 500, not a dropped connection.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			a.errs.write(w, r, fmt.Errorf("panic: %v", p))
		}
	}()
	if a.failed.Load() {
		a.errs.write(w, r, ErrUnavailable)
		return
	}
	if h, pattern := a.mux.Handler(r); pattern == "" {
		a.unmatched(w, r, h)
		return
	}
	a.mux.ServeHTTP(w, r)
}

// unmatched runs the mux's answer for a request no route matched into a buffer:
// a 404 or 405 is rewritten as a problem, anything else (a redirect to the
// canonical path) is passed on as it is.
func (a *App) unmatched(w http.ResponseWriter, r *http.Request, h http.Handler) {
	rec := &recorder{header: http.Header{}}
	h.ServeHTTP(rec, r)
	switch rec.status {
	case http.StatusNotFound:
		a.errs.write(w, r, ErrNotFound)
	case http.StatusMethodNotAllowed:
		w.Header().Set("Allow", rec.header.Get("Allow"))
		a.errs.write(w, r, ErrMethodNotAllowed)
	default:
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body.Bytes())
	}
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

// OnShutdown adds work to Shutdown, after every module has stopped: closing the
// database, flushing a log. Hooks run in reverse order of adding, like defer.
func (a *App) OnShutdown(fn func(context.Context) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if fn != nil {
		a.hooks = append(a.hooks, fn)
	}
}

// Start starts every module that is a Starter, in order. If one fails, those
// already started are stopped again, the error is returned, and from then on the
// app answers every request 503 (ErrUnavailable): its handlers would meet
// modules that are not running. Run calls it; when collage serves the app, the
// collage-gesso plugin does.
func (a *App) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running || a.shutdown {
		return errors.New("gesso: Start called twice")
	}
	for _, m := range a.modules {
		if s, ok := m.(Starter); ok {
			if err := s.Start(ctx); err != nil {
				stopErr := a.stopModules(ctx)
				a.failed.Store(true)
				return errors.Join(fmt.Errorf("gesso: start %T: %w", m, err), stopErr)
			}
		}
		a.started = append(a.started, m)
	}
	a.running = true
	return nil
}

// Shutdown stops the started modules in reverse order, then runs the OnShutdown
// hooks. Every step runs even when one fails; the errors are joined. Stop the
// HTTP server before calling it, so no request meets a stopped module.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shutdown {
		return nil
	}
	a.shutdown = true
	a.running = false
	errs := []error{a.stopModules(ctx)}
	for _, fn := range slices.Backward(a.hooks) {
		errs = append(errs, fn(ctx))
	}
	return errors.Join(errs...)
}

func (a *App) stopModules(ctx context.Context) error {
	var errs []error
	for _, m := range slices.Backward(a.started) {
		if s, ok := m.(Stopper); ok {
			if err := s.Stop(ctx); err != nil {
				errs = append(errs, fmt.Errorf("gesso: stop %T: %w", m, err))
			}
		}
	}
	a.started = nil
	return errors.Join(errs...)
}

// Run serves the app as its own process: it starts the modules, listens on
// Config.Addr, and on SIGINT or SIGTERM shuts down in order — the HTTP server
// (draining requests), then the modules, then the OnShutdown hooks.
func (a *App) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.serve(ctx, &http.Server{Addr: a.cfg.Addr, Handler: a, ReadHeaderTimeout: 10 * time.Second})
}

func (a *App) serve(ctx context.Context, srv *http.Server) error {
	if err := a.Start(ctx); err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() {
		a.cfg.Logger.Info("gesso: listening", "addr", srv.Addr)
		serveErr <- srv.ListenAndServe()
	}()
	var errs []error
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		errs = append(errs, fmt.Errorf("gesso: http shutdown: %w", err))
	}
	errs = append(errs, a.Shutdown(sctx))
	return errors.Join(errs...)
}
