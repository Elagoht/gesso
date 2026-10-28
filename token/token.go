// Package token issues opaque, revocable bearer tokens.
//
// A token is 32 random bytes, prefixed "gso_" so secret scanners can find a
// leaked one. Only its SHA-256 is stored: a database dump holds no usable token.
// Every check goes to the store, so revoking — one token at logout, every token
// of a subject at a password change — takes effect on the next request.
//
// The Issuer is a gesso.Authenticator for the Authorization: Bearer header. It
// reads nothing else, cookies least of all: see DESIGN §11.
package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/clock"
)

// Prefix begins every token.
const Prefix = "gso_"

// Record is what a token stands for.
type Record struct {
	Subject   string
	Roles     []string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store keeps records under the hash of their token.
type Store interface {
	Save(ctx context.Context, hash string, r Record) error
	// Find returns the record under hash, or ErrNotFound.
	Find(ctx context.Context, hash string) (Record, error)
	// Delete removes hash. A missing hash is not an error.
	Delete(ctx context.Context, hash string) error
	// DeleteSubject removes every record of subject.
	DeleteSubject(ctx context.Context, subject string) error
	// DeleteExpired removes records that expired at or before t, and counts them.
	DeleteExpired(ctx context.Context, t time.Time) (int64, error)
}

var (
	// ErrNotFound is a Store's answer for a hash it does not hold.
	ErrNotFound = errors.New("token: not found")
	// ErrInvalid is Verify's answer for a token that is malformed, unknown,
	// revoked or expired; which of them is not told.
	ErrInvalid = errors.New("token: invalid")
)

// Issuer issues and verifies tokens.
type Issuer struct {
	store Store
	clock clock.Clock
	ttl   time.Duration
}

// NewIssuer issues tokens that live for ttl, kept in store, timed by c (nil for
// the system clock).
func NewIssuer(store Store, c clock.Clock, ttl time.Duration) *Issuer {
	if c == nil {
		c = clock.System
	}
	return &Issuer{store: store, clock: c, ttl: ttl}
}

// Issue makes a token for subject. The token is returned once and never stored;
// give it to the client.
func (i *Issuer) Issue(ctx context.Context, subject string, roles ...string) (string, Record, error) {
	if subject == "" {
		return "", Record{}, errors.New("token: empty subject")
	}
	for _, r := range roles {
		if r == "" || strings.ContainsAny(r, ",\n") {
			return "", Record{}, fmt.Errorf("token: role %q is empty or holds a comma", r)
		}
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", Record{}, err
	}
	raw := Prefix + base64.RawURLEncoding.EncodeToString(secret[:])
	now := i.clock.Now().UTC().Truncate(time.Millisecond)
	rec := Record{Subject: subject, Roles: roles, CreatedAt: now, ExpiresAt: now.Add(i.ttl)}
	if err := i.store.Save(ctx, Hash(raw), rec); err != nil {
		return "", Record{}, fmt.Errorf("token: save: %w", err)
	}
	return raw, rec, nil
}

// Verify returns the record raw stands for. A malformed, unknown, revoked or
// expired token is ErrInvalid; a store that fails is its own error.
func (i *Issuer) Verify(ctx context.Context, raw string) (Record, error) {
	if !wellFormed(raw) {
		return Record{}, ErrInvalid
	}
	hash := Hash(raw)
	rec, err := i.store.Find(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		return Record{}, ErrInvalid
	}
	if err != nil {
		return Record{}, fmt.Errorf("token: find: %w", err)
	}
	if !i.clock.Now().Before(rec.ExpiresAt) {
		_ = i.store.Delete(ctx, hash)
		return Record{}, ErrInvalid
	}
	return rec, nil
}

// Revoke ends raw: logout.
func (i *Issuer) Revoke(ctx context.Context, raw string) error {
	if !wellFormed(raw) {
		return nil
	}
	return i.store.Delete(ctx, Hash(raw))
}

// RevokeSubject ends every token of subject: a password change, a ban.
func (i *Issuer) RevokeSubject(ctx context.Context, subject string) error {
	return i.store.DeleteSubject(ctx, subject)
}

// Purge removes the expired records; run it from a job now and then. Verify
// removes an expired token it meets, but one never presented again stays.
func (i *Issuer) Purge(ctx context.Context) (int64, error) {
	return i.store.DeleteExpired(ctx, i.clock.Now())
}

// Authenticate reads the Authorization: Bearer token. No Authorization header,
// or one of another scheme, is anonymous; a bearer token that does not verify is
// gesso.ErrInvalidToken, a 401.
func (i *Issuer) Authenticate(r *http.Request) (gesso.Identity, bool, error) {
	scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return gesso.Identity{}, false, nil
	}
	rec, err := i.Verify(r.Context(), strings.TrimSpace(raw))
	if errors.Is(err, ErrInvalid) {
		return gesso.Identity{}, false, gesso.ErrInvalidToken
	}
	if err != nil {
		return gesso.Identity{}, false, err
	}
	return gesso.Identity{Subject: rec.Subject, Roles: rec.Roles}, true, nil
}

// FromRequest is the bearer token a request carries, for a logout handler to
// revoke: gesso.Request(ctx) gives the request.
func FromRequest(r *http.Request) string {
	scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(raw)
}

// Hash is the key a token is stored under.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func wellFormed(raw string) bool {
	rest, ok := strings.CutPrefix(raw, Prefix)
	if !ok || len(rest) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(rest)
	return err == nil
}
