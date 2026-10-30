// Package auth is sign-in, ready to use: a module that serves Login, Logout, Me
// and ChangePassword, issues the bearer tokens of package token, and is the
// app's gesso.Authenticator.
//
//	app.Register(auth.New(), …)
//
// Registered with no Options on an app with a Config.Database, it keeps the
// users in a table (NewSQL) and the tokens beside them, makes both tables as
// the app starts (Migrate), adds the "users" command, and is the app's
// authenticator. Options set another source of users, another token store.
//
// Who may sign in is a Users: NewSingle, one user from the server's settings;
// NewSQL, a table of users with argon2id password hashes, managed with its
// Command (app users add <name>); or a source of the application's own.
//
// There is no sign-up endpoint: an API that lets anyone make an account says so
// with an endpoint of its own, calling SQL.Create.
//
// Sign-in attempts are limited per client address, and per address and
// username together — never per username alone, which would let anyone lock
// the user out by guessing wrong on purpose.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/migrate"
	"github.com/Elagoht/gesso/ratelimit"
	"github.com/Elagoht/gesso/token"
)

// Options configures the module.
type Options struct {
	// DB keeps the tokens, in the gesso_tokens table, written as Dialect
	// does. Required, unless TokenStore is given.
	DB      *sql.DB
	Dialect migrate.Dialect
	// Users are who may sign in. Required.
	Users Users
	// JWT, when set, signs in with JSON Web Tokens — a short access token and a
	// refresh token, with POST /auth/refresh — instead of opaque tokens.
	JWT *JWT
	// TokenTTL is how long a sign-in lasts. Zero: 30 days.
	TokenTTL time.Duration
	// Attempts is how many sign-ins a client address may try per minute with
	// one username; it may try four times as many across usernames. Zero: 5.
	Attempts int
	// Limits keeps the attempts. Nil: ratelimit.NewMemory, each process its
	// own; ratelimit.NewSQL shares them between replicas.
	Limits ratelimit.Store
	// TrustedProxies are the proxies whose X-Forwarded-For names the client.
	TrustedProxies []netip.Prefix
	// TokenStore keeps the tokens instead of DB: token.NewMemory in a test.
	TokenStore token.Store
	// Clock tells the time. Nil: the system clock.
	Clock clock.Clock
	// PurgeEvery is how often the expired tokens are removed while the app
	// runs. Zero: an hour; negative: never — a job of the app's does it.
	PurgeEvery time.Duration
	// Logger receives the limiter's store errors. Nil: slog.Default().
	Logger *slog.Logger
}

// JWT are the settings of sign-in with JSON Web Tokens.
type JWT struct {
	// Secret signs them: at least 32 random bytes, JWT_SECRET say. The app
	// does not start without it; its commands run.
	Secret []byte
	// Access is how long an access token lasts (zero: 15 minutes), Refresh a
	// refresh token (zero: 30 days).
	Access, Refresh time.Duration
}

// sessions are the tokens sign-in gives: opaque ones, or JWTs.
type sessions interface {
	authenticate(r *http.Request) (gesso.Identity, bool, error)
	issue(ctx context.Context, u User) (ResSession, error)
	revoke(ctx context.Context, raw string) error
	revokeSubject(ctx context.Context, subject string) error
	purge(ctx context.Context) (int64, error)
}

type opaque struct{ *token.Issuer }

func (o opaque) authenticate(r *http.Request) (gesso.Identity, bool, error) {
	return o.Authenticate(r)
}

func (o opaque) issue(ctx context.Context, u User) (ResSession, error) {
	raw, rec, err := o.Issue(ctx, u.ID, u.Roles...)
	return ResSession{Token: raw, ExpiresAt: rec.ExpiresAt}, err
}

func (o opaque) revoke(ctx context.Context, raw string) error { return o.Revoke(ctx, raw) }

func (o opaque) revokeSubject(ctx context.Context, subject string) error {
	return o.RevokeSubject(ctx, subject)
}

func (o opaque) purge(ctx context.Context) (int64, error) { return o.Purge(ctx) }

type jwts struct{ *token.JWT }

func (j jwts) authenticate(r *http.Request) (gesso.Identity, bool, error) {
	return j.Authenticate(r)
}

func (j jwts) issue(ctx context.Context, u User) (ResSession, error) {
	p, err := j.Issue(ctx, u.ID, u.Roles...)
	return ResSession{Token: p.Access, ExpiresAt: p.AccessExpires, RefreshToken: p.Refresh, RefreshExpiresAt: p.RefreshExpires}, err
}

func (j jwts) revoke(ctx context.Context, raw string) error { return j.Revoke(ctx, raw) }

func (j jwts) revokeSubject(ctx context.Context, subject string) error {
	return j.RevokeSubject(ctx, subject)
}

func (j jwts) purge(ctx context.Context) (int64, error) { return j.Purge(ctx) }

// Module is sign-in: a gesso.Module, and the app's gesso.Authenticator.
type Module struct {
	opts    Options
	tokens  *token.Issuer // nil with JWTs
	jwt     *token.JWT    // nil with opaque tokens
	session sessions
	setup   error              // a JWT secret too weak: the app does not start
	perPair *ratelimit.Limiter // per address and username
	perAddr *ratelimit.Limiter // per address
	addr    func(*http.Request) string
	stop    context.CancelFunc
	done    chan struct{}
}

// New is the module; it is built from opts, at most one, when the app
// registers it.
func New(opts ...Options) *Module {
	m := &Module{}
	if len(opts) > 0 {
		m.opts = opts[0]
	}
	return m
}

// Init builds the module, taking from app what opts leave out: the database
// for the tokens and, with no Users, for a table of users with its command.
func (m *Module) Init(app *gesso.App) error {
	opts := m.opts
	if (opts.Users == nil || (opts.TokenStore == nil && opts.DB == nil)) && app.DB() != nil {
		if opts.DB == nil {
			opts.DB, opts.Dialect = app.DB(), app.Dialect()
		}
		if opts.Users == nil {
			users := NewSQL(opts.DB, opts.Dialect)
			opts.Users = users
			app.Schema(migrateUsers)
			app.Command("users", users.Command(os.Stdin))
		}
		switch {
		case opts.JWT != nil:
			app.Schema(token.MigrateJWT)
		case opts.TokenStore == nil:
			app.Schema(token.Migrate)
		}
	}
	built, err := build(opts)
	if err != nil {
		return err
	}
	*m = *built
	return nil
}

func build(opts Options) (*Module, error) {
	if opts.Users == nil {
		return nil, errors.New("auth: no users: give Options.Users, or the app a Config.Database")
	}
	if s, ok := opts.Users.(*Single); ok && s.empty() {
		return nil, errors.New("auth: NewSingle needs a username and a password")
	}
	store := opts.TokenStore
	if store == nil && opts.JWT == nil {
		if opts.DB == nil {
			return nil, errors.New("auth: the tokens need a database — Options.DB or the app's — or a TokenStore")
		}
		store = token.NewSQL(opts.DB, opts.Dialect)
	}
	if opts.JWT != nil && opts.DB == nil {
		return nil, errors.New("auth: the JWT denylist needs a database — Options.DB or the app's")
	}
	if opts.TokenTTL == 0 {
		opts.TokenTTL = 30 * 24 * time.Hour
	}
	if opts.Attempts == 0 {
		opts.Attempts = 5
	}
	if opts.Limits == nil {
		opts.Limits = ratelimit.NewMemory(0)
	}
	if opts.Clock == nil {
		opts.Clock = clock.System
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	limiter := func(n int) *ratelimit.Limiter {
		return ratelimit.New(ratelimit.Options{Requests: n, Per: time.Minute, Store: opts.Limits, Clock: opts.Clock, Logger: opts.Logger})
	}
	m := &Module{
		opts:    opts,
		perPair: limiter(opts.Attempts),
		perAddr: limiter(4 * opts.Attempts),
		addr:    func(r *http.Request) string { return ratelimit.ClientIP(r, opts.TrustedProxies) },
	}
	if opts.JWT == nil {
		m.tokens = token.NewIssuer(store, opts.Clock, opts.TokenTTL)
		m.session = opaque{m.tokens}
		return m, nil
	}
	access, refresh := opts.JWT.Access, opts.JWT.Refresh
	if access == 0 {
		access = 15 * time.Minute
	}
	if refresh == 0 {
		refresh = 30 * 24 * time.Hour
	}
	j, err := token.NewJWT(token.JWTOptions{Secret: opts.JWT.Secret, Access: access, Refresh: refresh,
		Deny: token.NewDenySQL(opts.DB, opts.Dialect), Clock: opts.Clock})
	if err != nil {
		// Its commands need no secret; serving does: Start says so.
		m.setup = fmt.Errorf("auth: %w (JWT_SECRET)", err)
		return m, nil
	}
	m.jwt, m.session = j, jwts{j}
	if s, ok := opts.Users.(*SQL); ok && s.RevokeSubject == nil {
		s.RevokeSubject = j.RevokeSubject
	}
	return m, nil
}

// Start removes the expired tokens now and every Options.PurgeEvery, until
// Stop: signing in leaves a row behind for each token, and one never presented
// again is never met to be removed.
func (m *Module) Start(ctx context.Context) error {
	if m.setup != nil {
		return m.setup
	}
	every := m.opts.PurgeEvery
	if every == 0 {
		every = time.Hour
	}
	if every < 0 || m.session == nil {
		return nil
	}
	ctx, m.stop = context.WithCancel(context.WithoutCancel(ctx))
	m.done = make(chan struct{})
	go func() {
		defer close(m.done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			if _, err := m.session.purge(ctx); err != nil && ctx.Err() == nil {
				m.opts.Logger.Error("auth: purge expired tokens", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

// Stop ends what Start began.
func (m *Module) Stop(ctx context.Context) error {
	if m.stop == nil {
		return nil
	}
	m.stop()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Users are who may sign in: a *SQL when the module made the table itself.
func (m *Module) Users() Users { return m.opts.Users }

// Tokens is the module's token issuer: Purge it from a scheduled job, revoke a
// subject's tokens when the application locks them out.
func (m *Module) Tokens() *token.Issuer { return m.tokens }

// Authenticate finds the request's identity from its bearer token, with the
// user's name and roles as they are now: a role taken away, a user deleted,
// count from the next request.
func (m *Module) Authenticate(r *http.Request) (gesso.Identity, bool, error) {
	if m.session == nil {
		return gesso.Identity{}, false, gesso.ErrUnavailable
	}
	id, ok, err := m.session.authenticate(r)
	if !ok || err != nil {
		return id, ok, err
	}
	u, err := m.opts.Users.Lookup(r.Context(), id.Subject)
	if errors.Is(err, ErrNoUser) {
		return gesso.Identity{}, false, gesso.ErrInvalidToken
	}
	if err != nil {
		return gesso.Identity{}, false, err
	}
	return gesso.Identity{Subject: u.ID, Name: u.Username, Roles: u.Roles}, true, nil
}

// Routes registers the endpoints; ChangePassword only when the Users are a
// PasswordChanger.
func (m *Module) Routes(r *gesso.Router) {
	m.SignInRoutes(r)
	Me.Handle(r, m.me, gesso.Guards(gesso.RequireIdentity), tag)
}

// tag puts sign-in's endpoints under auth in the OpenAPI document, whichever
// module registers them.
var tag = gesso.Tags("auth")

// SignInRoutes registers Login, Logout and ChangePassword but not Me: for a
// module of the app's own — a //gesso:auth model's — that answers /auth/me
// with its users' fields.
func (m *Module) SignInRoutes(r *gesso.Router) {
	signedIn := gesso.Guards(gesso.RequireIdentity)
	Login.Handle(r, m.login, tag)
	Logout.Handle(r, m.logout, signedIn, tag)
	if m.opts.JWT != nil {
		Refresh.Handle(r, m.refresh, tag)
	}
	if pc, ok := m.opts.Users.(PasswordChanger); ok {
		ChangePassword.Handle(r, func(ctx context.Context, in ReqChangePassword) (ResSession, error) {
			return m.changePassword(ctx, pc, in)
		}, signedIn, tag)
	}
}

func (m *Module) login(ctx context.Context, in ReqLogin) (ResSession, error) {
	addr := m.addr(gesso.Request(ctx))
	if err := m.allow(ctx, m.perAddr, "auth:addr:"+addr); err != nil {
		return ResSession{}, err
	}
	if err := m.allow(ctx, m.perPair, "auth:pair:"+addr+"\x00"+in.Username); err != nil {
		return ResSession{}, err
	}
	u, err := m.opts.Users.Authenticate(ctx, in.Username, in.Password)
	if err != nil {
		return ResSession{}, err
	}
	return m.issue(ctx, u)
}

// allow takes an attempt from l, or answers 429 with Retry-After. A limiter
// whose store fails lets the attempt through, as the middleware does: it must
// not stop every sign-in with it.
func (m *Module) allow(ctx context.Context, l *ratelimit.Limiter, key string) error {
	ok, wait, err := l.Allow(ctx, key)
	if err != nil {
		m.opts.Logger.ErrorContext(ctx, "auth: the rate limit's store failed; letting the attempt through", "error", err)
		return nil
	}
	if !ok {
		gesso.ResponseHeader(ctx).Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		return gesso.ErrTooManyRequests
	}
	return nil
}

func (m *Module) issue(ctx context.Context, u User) (ResSession, error) {
	if m.session == nil {
		return ResSession{}, gesso.ErrUnavailable
	}
	return m.session.issue(ctx, u)
}

func (m *Module) logout(ctx context.Context, _ gesso.Empty) (gesso.Empty, error) {
	return gesso.Empty{}, m.session.revoke(ctx, token.FromRequest(gesso.Request(ctx)))
}

// refresh trades a refresh token for a new pair, with the user's roles as
// they are now; a user deleted since has none.
func (m *Module) refresh(ctx context.Context, in ReqRefresh) (ResSession, error) {
	if m.jwt == nil {
		return ResSession{}, gesso.ErrUnavailable
	}
	sub, err := m.jwt.Subject(ctx, in.RefreshToken)
	if err != nil && !errors.Is(err, token.ErrInvalid) {
		return ResSession{}, err
	}
	var roles []string
	if err == nil {
		u, lerr := m.opts.Users.Lookup(ctx, sub)
		if errors.Is(lerr, ErrNoUser) {
			return ResSession{}, gesso.ErrInvalidToken
		}
		if lerr != nil {
			return ResSession{}, lerr
		}
		roles = u.Roles
	}
	// Refresh itself: a spent token is told apart from a bad one there, and
	// ends its session.
	p, err := m.jwt.Refresh(ctx, in.RefreshToken, roles...)
	if errors.Is(err, token.ErrInvalid) {
		return ResSession{}, gesso.ErrInvalidToken
	}
	if err != nil {
		return ResSession{}, err
	}
	return ResSession{Token: p.Access, ExpiresAt: p.AccessExpires, RefreshToken: p.Refresh, RefreshExpiresAt: p.RefreshExpires}, nil
}

func (m *Module) me(ctx context.Context, _ gesso.Empty) (ResMe, error) {
	id, _ := gesso.IdentityFrom(ctx)
	u, err := m.opts.Users.Lookup(ctx, id.Subject)
	if errors.Is(err, ErrNoUser) {
		// Signed in, and deleted since.
		return ResMe{}, gesso.ErrInvalidToken
	}
	if err != nil {
		return ResMe{}, err
	}
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	return ResMe{ID: u.ID, Username: u.Username, Roles: roles}, nil
}

// changePassword sets the new password and signs every device out — one that
// knew the old password may be someone else's — and this one back in.
func (m *Module) changePassword(ctx context.Context, pc PasswordChanger, in ReqChangePassword) (ResSession, error) {
	id, _ := gesso.IdentityFrom(ctx)
	if err := pc.ChangePassword(ctx, id.Subject, in.Current, in.New); err != nil {
		return ResSession{}, err
	}
	if err := m.session.revokeSubject(ctx, id.Subject); err != nil {
		return ResSession{}, err
	}
	u, err := m.opts.Users.Lookup(ctx, id.Subject)
	if err != nil {
		return ResSession{}, err
	}
	return m.issue(ctx, u)
}
