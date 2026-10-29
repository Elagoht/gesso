package gesso

import (
	"context"
	"net/http"
)

// Get registers fn for GET pattern on r, its input and output read from fn's
// types: gesso.Get(r, "/docs/{slug}", m.get). It returns the route as an
// Endpoint, for a caller in another process.
func Get[In, Out any](r *Router, pattern string, fn HandlerFunc[In, Out], opts ...HandleOption) Endpoint[In, Out] {
	return register[In, Out](r, http.MethodGet, pattern, fn, opts)
}

// Post registers fn for POST pattern on r; see Get.
func Post[In, Out any](r *Router, pattern string, fn HandlerFunc[In, Out], opts ...HandleOption) Endpoint[In, Out] {
	return register[In, Out](r, http.MethodPost, pattern, fn, opts)
}

// Put registers fn for PUT pattern on r; see Get.
func Put[In, Out any](r *Router, pattern string, fn HandlerFunc[In, Out], opts ...HandleOption) Endpoint[In, Out] {
	return register[In, Out](r, http.MethodPut, pattern, fn, opts)
}

// Patch registers fn for PATCH pattern on r; see Get.
func Patch[In, Out any](r *Router, pattern string, fn HandlerFunc[In, Out], opts ...HandleOption) Endpoint[In, Out] {
	return register[In, Out](r, http.MethodPatch, pattern, fn, opts)
}

// Delete registers fn for DELETE pattern on r; see Get.
func Delete[In, Out any](r *Router, pattern string, fn HandlerFunc[In, Out], opts ...HandleOption) Endpoint[In, Out] {
	return register[In, Out](r, http.MethodDelete, pattern, fn, opts)
}

func register[In, Out any](r *Router, method, pattern string, fn HandlerFunc[In, Out], opts []HandleOption) Endpoint[In, Out] {
	r.Handle(method, pattern, Handle(fn, opts...))
	return Endpoint[In, Out]{Method: method, Pattern: r.prefix + pattern}
}

// Allowed reports whether the request a typed handler answers passes guard: a
// field only some may see, a record only some may read. Outside a handler it
// is false.
func Allowed(ctx context.Context, guard Guard) bool {
	r := Request(ctx)
	return r != nil && guard(r) == nil
}
