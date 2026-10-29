package gesso

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
)

// Middleware wraps a handler. The first one added is the outermost.
type Middleware func(http.Handler) http.Handler

// Router registers routes on an App. A Group is a surface: routes under a prefix
// with a pipeline of their own (an app API, an admin API, a partner API with
// signed requests).
//
// A request passes, in order: the middleware (Use), the authenticator
// (Authenticate), the guards (Guard, then a route's own), the decoding and
// validation of a typed handler's input, the interceptors (Intercept), and the
// handler.
//
// Each of these applies to the routes registered after it, so they come first; one
// after a route or a group in the same router is reported by New, because it
// would silently skip what came before it.
type Router struct {
	app    *App
	prefix string
	mws    []Middleware
	auth   Authenticator
	guards []Guard
	icpts  []Interceptor
	used   bool // a route or group was registered here
}

// Use adds middleware to every route registered on this router and its groups
// from now on.
func (r *Router) Use(mws ...Middleware) {
	if r.late("Use") || hasNil(r, "middleware", mws) {
		return
	}
	r.mws = append(r.mws, mws...)
}

// Authenticate finds each request's identity with a, for IdentityFrom and the
// guards. It replaces an authenticator the router inherited.
func (r *Router) Authenticate(a Authenticator) {
	if r.late("Authenticate") {
		return
	}
	if a == nil {
		r.app.fail(fmt.Errorf("gesso: nil authenticator under %q", r.full("")))
		return
	}
	r.auth = a
}

// Guard adds guards to every route registered on this router and its groups
// from now on, typed or not.
func (r *Router) Guard(guards ...Guard) {
	if r.late("Guard") || hasNil(r, "guard", guards) {
		return
	}
	r.guards = append(r.guards, guards...)
}

// Intercept adds interceptors to every typed handler registered on this router
// and its groups from now on. The first one added is the outermost.
func (r *Router) Intercept(icpts ...Interceptor) {
	if r.late("Intercept") || hasNil(r, "interceptor", icpts) {
		return
	}
	r.icpts = append(r.icpts, icpts...)
}

func (r *Router) late(what string) bool {
	if r.used {
		r.app.fail(fmt.Errorf("gesso: %s under %q after its routes: set up the pipeline first", what, r.full("")))
	}
	return r.used
}

func hasNil[T any](r *Router, what string, fns []T) bool {
	for _, fn := range fns {
		if reflect.ValueOf(fn).IsNil() {
			r.app.fail(fmt.Errorf("gesso: nil %s under %q", what, r.full("")))
			return true
		}
	}
	return false
}

// Group registers a surface under prefix: fn adds its middleware and routes, which
// also run every middleware of r.
func (r *Router) Group(prefix string, fn func(*Router)) {
	if err := checkPath(prefix); err != nil {
		r.app.fail(fmt.Errorf("gesso: group %q: %w", prefix, err))
		return
	}
	r.used = true
	fn(&Router{
		app: r.app, prefix: r.prefix + strings.TrimSuffix(prefix, "/"), auth: r.auth,
		mws: slices.Clone(r.mws), guards: slices.Clone(r.guards), icpts: slices.Clone(r.icpts),
	})
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
		bound, err := b.bind(r.app, route{method: method, pattern: full, icpts: slices.Clone(r.icpts)})
		if err != nil {
			r.app.fail(fmt.Errorf("gesso: %s %s: %w", method, full, err))
			return
		}
		h = bound
		if d, ok := bound.(describer); ok {
			r.app.docs = append(r.app.docs, docRoute{method: method, pattern: full, module: r.app.registering, guarded: len(r.guards) > 0, op: d.describe()})
		}
	}
	r.app.noteRoute(method, full, r.guards, h)
	if len(r.guards) > 0 {
		h = guarded{guards: slices.Clone(r.guards), next: h, errs: &r.app.errs}
	}
	if r.auth != nil {
		h = authenticating{auth: r.auth, next: h, errs: &r.app.errs}
	} else {
		h = authenticating{app: r.app, next: h, errs: &r.app.errs}
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

// binder is a handler that learns its route when it is registered: a typed
// handler checks its path fields against the pattern and returns a copy bound to
// the route and the app's settings.
type binder interface {
	bind(app *App, rt route) (http.Handler, error)
}

type route struct {
	method  string
	pattern string
	icpts   []Interceptor
}
