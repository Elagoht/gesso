package gesso

import (
	"context"
	"net/http"
	"slices"
)

// Identity is who sent a request: the subject an Authenticator recognised and the
// roles it holds. What else is known about the subject (a user row, a tenant) the
// application loads by Subject.
type Identity struct {
	Subject string
	// Name is how the subject is called, a username, when the authenticator
	// knows it.
	Name  string
	Roles []string
}

// HasRole reports whether the identity holds role.
func (id Identity) HasRole(role string) bool { return slices.Contains(id.Roles, role) }

type identityKey struct{}

// IdentityFrom is the identity Router.Authenticate found for the request, or
// false for an anonymous one.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// WithIdentity is ctx carrying id, for tests and for an Authenticator of the
// application's own making.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// Authenticator finds who sent a request. It reports false when the request
// carries no credential: the request goes on anonymous. A credential that is
// there but wrong — malformed, expired, revoked — is an error (ErrInvalidToken),
// never anonymous: a client with a bad token is told so.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, bool, error)
}

// Guard decides whether a request may go on, before its input is read: a request
// that may not is refused without learning what its input should have been.
// Return an *Error — ErrUnauthenticated, ErrForbidden — to refuse.
type Guard func(r *http.Request) error

// The errors the built-in guards and authenticators answer with.
var (
	ErrUnauthenticated = Unauthorized("unauthenticated", "the request needs a valid credential")
	ErrInvalidToken    = Unauthorized("invalid_token", "the credential is malformed, expired or revoked")
	ErrForbidden       = Forbidden("forbidden", "the credential does not allow this")
)

// RequireIdentity refuses an anonymous request with 401.
func RequireIdentity(r *http.Request) error {
	if _, ok := IdentityFrom(r.Context()); !ok {
		return ErrUnauthenticated
	}
	return nil
}

// RequireRole refuses an anonymous request with 401, and one whose identity holds
// none of roles with 403.
func RequireRole(roles ...string) Guard {
	return func(r *http.Request) error {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			return ErrUnauthenticated
		}
		if slices.ContainsFunc(roles, id.HasRole) {
			return nil
		}
		return ErrForbidden
	}
}

// Call is the route an interceptor is wrapping.
type Call struct {
	Method  string
	Pattern string
}

// Interceptor wraps a typed handler after its input is decoded and validated:
// it may time the call, put a deadline on ctx, log or translate the error the
// handler returns. It calls next to run the handler (and the interceptors inside
// it) and returns what is to be answered: nil for the handler's output, an error
// for a problem.
//
// Interceptors wrap typed handlers only; a plain http.Handler has no call to
// wrap.
type Interceptor func(ctx context.Context, call Call, next func(context.Context) error) error

func intercept(ctx context.Context, call Call, icpts []Interceptor, last func(context.Context) error) error {
	next := last
	for _, ic := range slices.Backward(icpts) {
		inner := next
		next = func(ctx context.Context) error { return ic(ctx, call, inner) }
	}
	return next(ctx)
}

// guarded runs a router's guards in front of a handler of any kind.
type guarded struct {
	guards []Guard
	next   http.Handler
	errs   *errorWriter
}

func (g guarded) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, guard := range g.guards {
		if err := guard(r); err != nil {
			g.errs.write(w, r, err)
			return
		}
	}
	g.next.ServeHTTP(w, r)
}

// authenticating finds the request's identity and puts it in the context.
// authenticating finds the identity with auth, a router's own, or else with the
// app's as it is when the request comes: the sign-in module may be registered
// after the module whose routes these are.
type authenticating struct {
	auth Authenticator
	app  *App
	next http.Handler
	errs *errorWriter
}

func (a authenticating) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := a.auth
	if auth == nil {
		auth = a.app.authenticator
	}
	if auth == nil {
		a.next.ServeHTTP(w, r)
		return
	}
	id, ok, err := auth.Authenticate(r)
	if err != nil {
		a.errs.write(w, r, err)
		return
	}
	if ok {
		r = r.WithContext(WithIdentity(r.Context(), id))
	}
	a.next.ServeHTTP(w, r)
}
