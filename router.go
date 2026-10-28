package gesso

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Middleware wraps a handler. The first one added is the outermost.
type Middleware func(http.Handler) http.Handler

// Router registers routes on an App. A Group is a surface: routes under a prefix
// with a middleware stack of their own (an app API, an admin API, a partner API
// with signed requests).
//
// Middleware applies to the routes registered after it, so Use comes first; a
// Use after a route or a group in the same router is reported by New, because it
// would silently skip what came before it.
type Router struct {
	app    *App
	prefix string
	mws    []Middleware
	used   bool // a route or group was registered here
}

// Use adds middleware to every route registered on this router and its groups
// from now on.
func (r *Router) Use(mws ...Middleware) {
	if r.used {
		r.app.fail(fmt.Errorf("gesso: Use under %q after its routes: register middleware first", r.full("")))
		return
	}
	for _, mw := range mws {
		if mw == nil {
			r.app.fail(fmt.Errorf("gesso: nil middleware under %q", r.full("")))
			return
		}
	}
	r.mws = append(r.mws, mws...)
}

// Group registers a surface under prefix: fn adds its middleware and routes, which
// also run every middleware of r.
func (r *Router) Group(prefix string, fn func(*Router)) {
	if err := checkPath(prefix); err != nil {
		r.app.fail(fmt.Errorf("gesso: group %q: %w", prefix, err))
		return
	}
	r.used = true
	fn(&Router{app: r.app, prefix: r.prefix + strings.TrimSuffix(prefix, "/"), mws: slices.Clone(r.mws)})
}

// Handle registers h for method and pattern. A pattern is net/http's: "/events",
// "/events/{id}", "/files/{path...}"; a trailing "/" matches the whole subtree.
// A typed handler (Handle) has its path fields checked against the pattern.
func (r *Router) Handle(method, pattern string, h http.Handler) {
	full := r.full(pattern)
	if err := checkPath(pattern); err != nil {
		r.app.fail(fmt.Errorf("gesso: %s %s: %w", method, full, err))
		return
	}
	if h == nil {
		r.app.fail(fmt.Errorf("gesso: %s %s: %w", method, full, errors.New("nil handler")))
		return
	}
	r.used = true
	if b, ok := h.(binder); ok {
		if err := b.bind(r.app, full); err != nil {
			r.app.fail(fmt.Errorf("gesso: %s %s: %w", method, full, err))
			return
		}
	}
	for _, mw := range slices.Backward(r.mws) {
		h = mw(h)
	}
	r.app.register(method+" "+full, h)
}

// Get registers a GET route. net/http answers HEAD with it too.
func (r *Router) Get(pattern string, h http.Handler) { r.Handle(http.MethodGet, pattern, h) }

// Post registers a POST route.
func (r *Router) Post(pattern string, h http.Handler) { r.Handle(http.MethodPost, pattern, h) }

// Put registers a PUT route.
func (r *Router) Put(pattern string, h http.Handler) { r.Handle(http.MethodPut, pattern, h) }

// Patch registers a PATCH route.
func (r *Router) Patch(pattern string, h http.Handler) { r.Handle(http.MethodPatch, pattern, h) }

// Delete registers a DELETE route.
func (r *Router) Delete(pattern string, h http.Handler) { r.Handle(http.MethodDelete, pattern, h) }

func (r *Router) full(pattern string) string {
	return r.app.basePath + r.prefix + pattern
}

func checkPath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return errors.New("a path must begin with \"/\"")
	}
	if strings.ContainsAny(p, " \t") {
		return errors.New("a path holds no spaces; the method is given separately")
	}
	return nil
}

// binder is a handler that learns its route when it is registered: the typed
// handlers check their path fields against the pattern and take the app's
// settings.
type binder interface {
	bind(app *App, pattern string) error
}
