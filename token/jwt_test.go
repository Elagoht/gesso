package token

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso/clock"
)

func newTestJWT(t *testing.T) (*JWT, *clock.Manual) {
	t.Helper()
	c := clock.NewManual(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	j, err := NewJWT(JWTOptions{Secret: []byte(strings.Repeat("k", 32)), Access: 15 * time.Minute, Refresh: 24 * time.Hour, Deny: NewDenyMemory(), Clock: c})
	if err != nil {
		t.Fatal(err)
	}
	return j, c
}

func authed(j *JWT, raw string) error {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	_, ok, err := j.Authenticate(r)
	if err == nil && !ok {
		return errors.New("anonymous")
	}
	return err
}

func TestJWT(t *testing.T) {
	ctx := context.Background()
	if _, err := NewJWT(JWTOptions{Secret: []byte("short"), Access: time.Minute, Refresh: time.Hour, Deny: NewDenyMemory()}); !errors.Is(err, ErrWeakSecret) {
		t.Fatalf("a short secret: %v", err)
	}
	j, c := newTestJWT(t)
	p, err := j.Issue(ctx, "7", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := authed(j, p.Access); err != nil {
		t.Fatalf("the access token: %v", err)
	}
	if err := authed(j, p.Refresh); err == nil {
		t.Fatal("a refresh token authenticates")
	}
	if _, err := j.Refresh(ctx, p.Access); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an access token refreshes: %v", err)
	}

	// The signature, one byte flipped; another algorithm; none.
	parts := strings.Split(p.Access, ".")
	sig := []byte(parts[2])
	sig[3] ^= 1
	if err := authed(j, parts[0]+"."+parts[1]+"."+string(sig)); err == nil {
		t.Fatal("a flipped signature")
	}
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	if err := authed(j, none+"."+parts[1]+"."); err == nil {
		t.Fatal("alg none")
	}
	hs512 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS512","typ":"JWT"}`))
	if err := authed(j, hs512+"."+parts[1]+"."+parts[2]); err == nil {
		t.Fatal("another algorithm")
	}
	other, _ := NewJWT(JWTOptions{Secret: []byte(strings.Repeat("x", 32)), Access: 15 * time.Minute, Refresh: 24 * time.Hour, Deny: NewDenyMemory(), Clock: c})
	forged, _ := other.Issue(ctx, "7", "admin")
	if err := authed(j, forged.Access); err == nil {
		t.Fatal("another key's token")
	}

	// Refresh: a new pair, the old refresh token spent; used again, the
	// session ends.
	c.Advance(time.Minute)
	q, err := j.Refresh(ctx, p.Refresh, "admin")
	if err != nil || q.Access == p.Access {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := j.Refresh(ctx, p.Refresh); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a refresh token used twice: %v", err)
	}
	if err := authed(j, q.Access); err == nil {
		t.Fatal("a session whose refresh token was used twice goes on")
	}

	// Logout with the access token ends the refresh token too.
	p, _ = j.Issue(ctx, "8")
	if err := j.Revoke(ctx, p.Access); err != nil {
		t.Fatal(err)
	}
	if err := authed(j, p.Access); err == nil {
		t.Fatal("an access token after logout")
	}
	if _, err := j.Refresh(ctx, p.Refresh); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a refresh token after logout: %v", err)
	}

	// A password change ends what was issued before it, not after.
	p, _ = j.Issue(ctx, "9")
	c.Advance(time.Second)
	if err := j.RevokeSubject(ctx, "9"); err != nil {
		t.Fatal(err)
	}
	if err := authed(j, p.Access); err == nil {
		t.Fatal("a token from before the password change")
	}
	c.Advance(time.Second)
	n, _ := j.Issue(ctx, "9")
	if err := authed(j, n.Access); err != nil {
		t.Fatalf("a token from after: %v", err)
	}

	// Expired.
	c.Advance(16 * time.Minute)
	if err := authed(j, n.Access); err == nil {
		t.Fatal("an expired access token")
	}
	c.Advance(25 * time.Hour)
	if _, err := j.Refresh(ctx, n.Refresh); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an expired refresh token: %v", err)
	}
	if purged, _ := j.Purge(ctx); purged == 0 {
		t.Fatal("nothing purged")
	}
}
