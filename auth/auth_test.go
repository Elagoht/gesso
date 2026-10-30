package auth_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/auth"
	"github.com/Elagoht/gesso/auth/authtest"
	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/token"
)

func TestSingle(t *testing.T) {
	authtest.Run(t, func(*testing.T) auth.Users {
		return auth.NewSingle(authtest.Username, authtest.Password, authtest.Role)
	})
}

func TestMemoryUsers(t *testing.T) {
	authtest.Run(t, func(*testing.T) auth.Users { return newMemUsers() })
}

// memUsers is a PasswordChanger, for the module's ChangePassword.
type memUsers struct {
	users     map[string]auth.User
	passwords map[string]string
}

func newMemUsers() *memUsers {
	return &memUsers{
		users:     map[string]auth.User{"1": {ID: "1", Username: authtest.Username, Roles: []string{authtest.Role}}},
		passwords: map[string]string{"1": authtest.Password},
	}
}

func (m *memUsers) Authenticate(_ context.Context, username, password string) (auth.User, error) {
	for id, u := range m.users {
		if u.Username == username && m.passwords[id] == password {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrBadCredentials
}

func (m *memUsers) Lookup(_ context.Context, id string) (auth.User, error) {
	u, ok := m.users[id]
	if !ok {
		return auth.User{}, auth.ErrNoUser
	}
	return u, nil
}

func (m *memUsers) ChangePassword(_ context.Context, id, current, next string) error {
	if m.passwords[id] != current {
		return auth.ErrBadCredentials
	}
	m.passwords[id] = next
	return nil
}

type fixture struct {
	api   *gesso.Remote
	clock *clock.Manual
	authn *auth.Module
}

func newAPI(t *testing.T, users auth.Users) fixture {
	t.Helper()
	c := clock.NewManual(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	authn := auth.New(auth.Options{Users: users, TokenStore: token.NewMemory(), Clock: c})
	app, err := gesso.New(gesso.Config{}, authn)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return fixture{gesso.NewLocal(app), c, authn}
}

func (f fixture) login(t *testing.T, password string, opts ...gesso.CallOption) gesso.CallOption {
	t.Helper()
	s, err := auth.Login.Call(context.Background(), f.api, auth.ReqLogin{Username: authtest.Username, Password: password}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if s.ExpiresAt != f.clock.Now().Add(30*24*time.Hour) {
		t.Fatalf("a sign-in lasts 30 days: %v", s.ExpiresAt)
	}
	return gesso.Bearer(s.Token)
}

func TestSignInAndOut(t *testing.T) {
	ctx := context.Background()
	f := newAPI(t, auth.NewSingle(authtest.Username, authtest.Password, authtest.Role))
	if _, err := auth.Me.Call(ctx, f.api, gesso.Empty{}); !errors.Is(err, gesso.ErrUnauthenticated) {
		t.Fatalf("me, signed out: %v", err)
	}
	if _, err := auth.Login.Call(ctx, f.api, auth.ReqLogin{Username: authtest.Username, Password: "wrong"}); !errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("a wrong password: %v", err)
	}
	me := f.login(t, authtest.Password)
	got, err := auth.Me.Call(ctx, f.api, gesso.Empty{}, me)
	if err != nil || got.ID != authtest.Username || got.Username != authtest.Username || !slices.Equal(got.Roles, []string{authtest.Role}) {
		t.Fatalf("me: %+v %v", got, err)
	}
	if _, err := auth.Logout.Call(ctx, f.api, gesso.Empty{}, me); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Me.Call(ctx, f.api, gesso.Empty{}, me); !errors.Is(err, gesso.ErrInvalidToken) {
		t.Fatalf("me, after logout: %v", err)
	}
	// A Single cannot change its password: it is a setting.
	if _, err := auth.ChangePassword.Call(ctx, f.api, auth.ReqChangePassword{Current: authtest.Password, New: "long enough now"}, f.login(t, authtest.Password)); err == nil {
		t.Fatal("ChangePassword is served for a Single")
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	f := newAPI(t, newMemUsers())
	other := f.login(t, authtest.Password) // another device
	me := f.login(t, authtest.Password)
	const next = "battery staple horse"
	if _, err := auth.ChangePassword.Call(ctx, f.api, auth.ReqChangePassword{Current: "wrong", New: next}, me); !errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("a wrong current password: %v", err)
	}
	if _, err := auth.ChangePassword.Call(ctx, f.api, auth.ReqChangePassword{Current: authtest.Password, New: "short"}, me); !errors.Is(err, gesso.ErrInvalidInput) {
		t.Fatalf("a short password: %v", err)
	}
	s, err := auth.ChangePassword.Call(ctx, f.api, auth.ReqChangePassword{Current: authtest.Password, New: next}, me)
	if err != nil {
		t.Fatal(err)
	}
	for name, opt := range map[string]gesso.CallOption{"this device's old token": me, "another device's": other} {
		if _, err := auth.Me.Call(ctx, f.api, gesso.Empty{}, opt); !errors.Is(err, gesso.ErrInvalidToken) {
			t.Errorf("%s still signs in: %v", name, err)
		}
	}
	if _, err := auth.Me.Call(ctx, f.api, gesso.Empty{}, gesso.Bearer(s.Token)); err != nil {
		t.Fatalf("the token ChangePassword answers with: %v", err)
	}
}

func TestDeletedUser(t *testing.T) {
	users := newMemUsers()
	f := newAPI(t, users)
	me := f.login(t, authtest.Password)
	delete(users.users, "1")
	if _, err := auth.Me.Call(context.Background(), f.api, gesso.Empty{}, me); !errors.Is(err, gesso.ErrInvalidToken) {
		t.Fatalf("me, deleted since: %v", err)
	}
}

// Five a minute per address and username; twenty per address. Never per
// username alone: a stranger's wrong guesses do not lock the user out.
func TestAttemptsAreLimited(t *testing.T) {
	ctx := context.Background()
	f := newAPI(t, auth.NewSingle(authtest.Username, authtest.Password))
	attacker, user := gesso.ClientAddr("203.0.113.7"), gesso.ClientAddr("198.51.100.1")
	guess := func(username string, from gesso.CallOption) error {
		_, err := auth.Login.Call(ctx, f.api, auth.ReqLogin{Username: username, Password: "guess"}, from)
		return err
	}
	for i := range 5 {
		if err := guess(authtest.Username, attacker); !errors.Is(err, auth.ErrBadCredentials) {
			t.Fatalf("guess %d: %v", i+1, err)
		}
	}
	if err := guess(authtest.Username, attacker); !errors.Is(err, gesso.ErrTooManyRequests) {
		t.Fatalf("the sixth guess: %v", err)
	}
	f.login(t, authtest.Password, user) // the user, elsewhere, signs in

	// Other usernames from the same address, up to twenty attempts in all —
	// the refused sixth among them.
	for i := range 14 {
		if err := guess("user"+strconv.Itoa(i), attacker); !errors.Is(err, auth.ErrBadCredentials) {
			t.Fatalf("username %d: %v", i, err)
		}
	}
	if err := guess("one-more", attacker); !errors.Is(err, gesso.ErrTooManyRequests) {
		t.Fatalf("past twenty from one address: %v", err)
	}
	f.clock.Advance(time.Minute)
	if err := guess(authtest.Username, attacker); !errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("a minute on: %v", err)
	}
}

func TestNewRefuses(t *testing.T) {
	for name, opts := range map[string]auth.Options{
		"no users":       {TokenStore: token.NewMemory()},
		"empty username": {Users: auth.NewSingle("", "pass"), TokenStore: token.NewMemory()},
		"empty password": {Users: auth.NewSingle("ada", ""), TokenStore: token.NewMemory()},
		"no database":    {Users: auth.NewSingle("ada", "pass")},
	} {
		if _, err := gesso.New(gesso.Config{}, auth.New(opts)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// purges is a token store that reports its purges.
type purges struct {
	*token.Memory
	done chan time.Time
}

func (p purges) DeleteExpired(ctx context.Context, t time.Time) (int64, error) {
	p.done <- t
	return p.Memory.DeleteExpired(ctx, t)
}

// The module removes the expired tokens while the app runs, and stops when it
// does.
func TestPurgeExpired(t *testing.T) {
	store := purges{token.NewMemory(), make(chan time.Time, 8)}
	authn := auth.New(auth.Options{Users: auth.NewSingle("ada", "a long password"), TokenStore: store, PurgeEvery: 10 * time.Millisecond})
	app, err := gesso.New(gesso.Config{}, authn)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-store.done:
		case <-time.After(5 * time.Second):
			t.Fatal("no purge")
		}
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for len(store.done) > 0 {
		<-store.done
	}
	time.Sleep(50 * time.Millisecond)
	if len(store.done) != 0 {
		t.Fatal("purges after shutdown")
	}
}
