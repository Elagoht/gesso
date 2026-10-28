// Package tokentest holds every token.Store to the same contract, through an
// Issuer driven by a manual clock.
//
//	tokentest.Run(t, func(t *testing.T) token.Store { return token.NewSQL(tx, migrate.Postgres) })
package tokentest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/token"
)

// Run checks the contract on a fresh, empty store from open for each case.
func Run(t *testing.T, open func(t *testing.T) token.Store) {
	ctx := context.Background()
	start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	setup := func(t *testing.T) (*token.Issuer, *clock.Manual, token.Store) {
		clk := clock.NewManual(start)
		s := open(t)
		return token.NewIssuer(s, clk, time.Hour), clk, s
	}

	t.Run("issue then verify", func(t *testing.T) {
		iss, _, _ := setup(t)
		raw, rec, err := iss.Issue(ctx, "ada", "admin", "editor")
		if err != nil {
			t.Fatal(err)
		}
		got, err := iss.Verify(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		if got.Subject != "ada" || !slices.Equal(got.Roles, []string{"admin", "editor"}) ||
			!got.CreatedAt.Equal(rec.CreatedAt) || !got.ExpiresAt.Equal(start.Add(time.Hour)) {
			t.Fatalf("got %+v, issued %+v", got, rec)
		}
	})

	t.Run("no roles", func(t *testing.T) {
		iss, _, _ := setup(t)
		raw, _, _ := iss.Issue(ctx, "bob")
		if got, err := iss.Verify(ctx, raw); err != nil || len(got.Roles) != 0 {
			t.Fatalf("got %+v %v", got, err)
		}
	})

	t.Run("only the hash is stored", func(t *testing.T) {
		iss, _, s := setup(t)
		raw, _, _ := iss.Issue(ctx, "ada")
		if _, err := s.Find(ctx, raw); !errors.Is(err, token.ErrNotFound) {
			t.Fatalf("the raw token is a key: %v", err)
		}
		if _, err := s.Find(ctx, token.Hash(raw)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		iss, clk, _ := setup(t)
		raw, _, _ := iss.Issue(ctx, "ada")
		clk.Advance(time.Hour - time.Millisecond)
		if _, err := iss.Verify(ctx, raw); err != nil {
			t.Fatalf("expired early: %v", err)
		}
		clk.Advance(time.Millisecond)
		if _, err := iss.Verify(ctx, raw); !errors.Is(err, token.ErrInvalid) {
			t.Fatalf("valid at its expiry: %v", err)
		}
	})

	t.Run("revoke one", func(t *testing.T) {
		iss, _, _ := setup(t)
		a, _, _ := iss.Issue(ctx, "ada")
		b, _, _ := iss.Issue(ctx, "ada")
		if err := iss.Revoke(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := iss.Verify(ctx, a); !errors.Is(err, token.ErrInvalid) {
			t.Fatalf("revoked token verifies: %v", err)
		}
		if _, err := iss.Verify(ctx, b); err != nil {
			t.Fatalf("the other session ended too: %v", err)
		}
	})

	t.Run("revoke a subject", func(t *testing.T) {
		iss, _, _ := setup(t)
		a, _, _ := iss.Issue(ctx, "ada")
		b, _, _ := iss.Issue(ctx, "ada")
		c, _, _ := iss.Issue(ctx, "bob")
		if err := iss.RevokeSubject(ctx, "ada"); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{a, b} {
			if _, err := iss.Verify(ctx, raw); !errors.Is(err, token.ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		}
		if _, err := iss.Verify(ctx, c); err != nil {
			t.Fatalf("another subject was revoked: %v", err)
		}
	})

	t.Run("purge", func(t *testing.T) {
		iss, clk, _ := setup(t)
		_, _, _ = iss.Issue(ctx, "old")
		clk.Advance(30 * time.Minute)
		fresh, _, _ := iss.Issue(ctx, "new")
		clk.Advance(30 * time.Minute)
		n, err := iss.Purge(ctx)
		if err != nil || n != 1 {
			t.Fatalf("purged %d, %v", n, err)
		}
		if _, err := iss.Verify(ctx, fresh); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("malformed and unknown", func(t *testing.T) {
		iss, _, _ := setup(t)
		raw, _, _ := iss.Issue(ctx, "ada")
		for _, bad := range []string{"", "gso_", raw + "x", raw[:len(raw)-1], "xyz" + raw[3:], token.Prefix + "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"} {
			if _, err := iss.Verify(ctx, bad); !errors.Is(err, token.ErrInvalid) {
				t.Errorf("%q: %v", bad, err)
			}
		}
		other, _, _ := token.NewIssuer(token.NewMemory(), nil, time.Hour).Issue(ctx, "ada")
		if _, err := iss.Verify(ctx, other); !errors.Is(err, token.ErrInvalid) {
			t.Fatalf("a token this store never issued: %v", err)
		}
	})

	t.Run("authenticate", func(t *testing.T) {
		iss, _, _ := setup(t)
		raw, _, _ := iss.Issue(ctx, "ada", "admin")
		req := func(auth string) *http.Request {
			r := httptest.NewRequest("GET", "/", nil)
			if auth != "" {
				r.Header.Set("Authorization", auth)
			}
			return r
		}
		if _, ok, err := iss.Authenticate(req("")); ok || err != nil {
			t.Fatalf("no header is anonymous: %v %v", ok, err)
		}
		if _, ok, err := iss.Authenticate(req("Basic dXNlcjpwYXNz")); ok || err != nil {
			t.Fatalf("another scheme is not ours: %v %v", ok, err)
		}
		if id, ok, err := iss.Authenticate(req("Bearer " + raw)); !ok || err != nil || id.Subject != "ada" || !id.HasRole("admin") {
			t.Fatalf("got %+v %v %v", id, ok, err)
		}
		if _, _, err := iss.Authenticate(req("Bearer forged")); !errors.Is(err, gesso.ErrInvalidToken) {
			t.Fatalf("a bad bearer token is a 401, never anonymous: %v", err)
		}
	})
}
