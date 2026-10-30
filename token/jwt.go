package token

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
)

// JWT issues and verifies signed tokens in pairs: a short access token, sent
// with every request, and a longer refresh token, traded for a new pair. They
// are HS256 JSON Web Tokens; what they may no longer do — a logout, a refresh
// token used, a password changed — is kept in a denylist until they expire.
//
// A pair shares a session id: logging out with the access token ends the
// refresh token too. A refresh token works once; used again, it ends its
// session, since one of the two who hold it is not its owner.
type JWT struct {
	key             []byte
	access, refresh time.Duration
	clock           clock.Clock
	deny            Denylist
}

// JWTOptions configures a JWT.
type JWTOptions struct {
	// Secret signs the tokens: at least 32 random bytes, kept from everyone.
	Secret []byte
	// Access is how long an access token lasts, Refresh a refresh token.
	Access, Refresh time.Duration
	// Deny keeps what the tokens may no longer do: NewDenySQL, or NewDenyMemory.
	Deny  Denylist
	Clock clock.Clock
}

// Pair is a signed-in session's tokens.
type Pair struct {
	Access         string
	AccessExpires  time.Time
	Refresh        string
	RefreshExpires time.Time
}

// ErrWeakSecret is a secret shorter than 32 bytes.
var ErrWeakSecret = errors.New("token: the JWT secret is shorter than 32 bytes")

// NewJWT checks o and makes a JWT.
func NewJWT(o JWTOptions) (*JWT, error) {
	if len(o.Secret) < 32 {
		return nil, ErrWeakSecret
	}
	if o.Access <= 0 || o.Refresh <= o.Access {
		return nil, errors.New("token: a JWT access token lasts a while, and a refresh token longer")
	}
	if o.Deny == nil {
		return nil, errors.New("token: JWTOptions.Deny is nil")
	}
	if o.Clock == nil {
		o.Clock = clock.System
	}
	return &JWT{key: o.Secret, access: o.Access, refresh: o.Refresh, clock: o.Clock, deny: o.Deny}, nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type claims struct {
	Sub   string   `json:"sub"`
	Roles []string `json:"roles,omitempty"`
	Typ   string   `json:"typ"` // access or refresh
	Sid   string   `json:"sid"`
	Jti   string   `json:"jti"`
	Iat   int64    `json:"iat"`
	IatNs int64    `json:"iat_ns"` // when, to the nanosecond: a password change ends what came before
	Exp   int64    `json:"exp"`
}

const (
	typAccess  = "access"
	typRefresh = "refresh"
)

var jwtHead = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Issue begins a session for subject.
func (j *JWT) Issue(ctx context.Context, subject string, roles ...string) (Pair, error) {
	if subject == "" {
		return Pair{}, errors.New("token: empty subject")
	}
	sid, err := randomID()
	if err != nil {
		return Pair{}, err
	}
	return j.pair(subject, roles, sid)
}

func (j *JWT) pair(subject string, roles []string, sid string) (Pair, error) {
	now := j.clock.Now()
	access, aexp, err := j.sign(claims{Sub: subject, Roles: roles, Typ: typAccess, Sid: sid}, now, j.access)
	if err != nil {
		return Pair{}, err
	}
	refresh, rexp, err := j.sign(claims{Sub: subject, Typ: typRefresh, Sid: sid}, now, j.refresh)
	if err != nil {
		return Pair{}, err
	}
	return Pair{Access: access, AccessExpires: aexp, Refresh: refresh, RefreshExpires: rexp}, nil
}

func (j *JWT) sign(c claims, now time.Time, ttl time.Duration) (string, time.Time, error) {
	jti, err := randomID()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := now.Add(ttl).Truncate(time.Second)
	c.Jti, c.Iat, c.IatNs, c.Exp = jti, now.Unix(), now.UnixNano(), exp.Unix()
	payload, err := json.Marshal(c)
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := jwtHead + "." + base64.RawURLEncoding.EncodeToString(payload)
	return unsigned + "." + j.mac(unsigned), exp, nil
}

func (j *JWT) mac(unsigned string) string {
	h := hmac.New(sha256.New, j.key)
	h.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// parse checks raw's form, algorithm, signature and expiry, and that it is a
// token of typ; ErrInvalid otherwise.
func (j *JWT) parse(raw, typ string) (claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return claims{}, ErrInvalid
	}
	headJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims{}, ErrInvalid
	}
	var h jwtHeader
	if json.Unmarshal(headJSON, &h) != nil || h.Alg != "HS256" {
		// Never "none", never another algorithm a client names.
		return claims{}, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims{}, ErrInvalid
	}
	want, _ := base64.RawURLEncoding.DecodeString(j.mac(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, want) {
		return claims{}, ErrInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, ErrInvalid
	}
	var c claims
	if json.Unmarshal(payload, &c) != nil || c.Typ != typ || c.Sub == "" || c.Sid == "" || c.Jti == "" {
		return claims{}, ErrInvalid
	}
	now := j.clock.Now()
	if now.Unix() >= c.Exp || c.Iat > now.Add(time.Minute).Unix() {
		return claims{}, ErrInvalid
	}
	return c, nil
}

// verify is parse, and that the denylist does not end it.
func (j *JWT) verify(ctx context.Context, raw, typ string) (claims, error) {
	c, err := j.parse(raw, typ)
	if err != nil {
		return c, err
	}
	found, err := j.deny.Check(ctx, "jti:"+c.Jti, "sid:"+c.Sid, "sub:"+c.Sub)
	if err != nil {
		return c, fmt.Errorf("token: denylist: %w", err)
	}
	if _, ok := found["jti:"+c.Jti]; ok {
		return c, ErrInvalid
	}
	if _, ok := found["sid:"+c.Sid]; ok {
		return c, ErrInvalid
	}
	if before, ok := found["sub:"+c.Sub]; ok && c.IatNs <= before {
		return c, ErrInvalid
	}
	return c, nil
}

// Authenticate reads the Authorization: Bearer access token, as
// Issuer.Authenticate does.
func (j *JWT) Authenticate(r *http.Request) (gesso.Identity, bool, error) {
	raw, present := bearer(r)
	if !present {
		return gesso.Identity{}, false, nil
	}
	c, err := j.verify(r.Context(), raw, typAccess)
	if errors.Is(err, ErrInvalid) {
		return gesso.Identity{}, false, gesso.ErrInvalidToken
	}
	if err != nil {
		return gesso.Identity{}, false, err
	}
	return gesso.Identity{Subject: c.Sub, Roles: c.Roles}, true, nil
}

// Refresh trades a refresh token for a new pair of its session; roles are
// what the caller knows them to be now. A refresh token used before ends the
// session and is ErrInvalid.
func (j *JWT) Refresh(ctx context.Context, raw string, roles ...string) (Pair, error) {
	c, err := j.parse(raw, typRefresh)
	if err != nil {
		return Pair{}, err
	}
	reused := func() (Pair, error) {
		// Used twice: whoever holds it besides its owner is not to keep it.
		_ = j.deny.Set(ctx, "sid:"+c.Sid, 0, j.clock.Now().Add(j.refresh))
		return Pair{}, ErrInvalid
	}
	if _, err := j.verify(ctx, raw, typRefresh); errors.Is(err, ErrInvalid) {
		found, ferr := j.deny.Check(ctx, "jti:"+c.Jti, "sid:"+c.Sid, "sub:"+c.Sub)
		if _, spent := found["jti:"+c.Jti]; ferr == nil && spent {
			return reused()
		}
		return Pair{}, err
	} else if err != nil {
		return Pair{}, err
	}
	err = j.deny.Add(ctx, "jti:"+c.Jti, 0, time.Unix(c.Exp, 0))
	if errors.Is(err, ErrDenied) {
		return reused()
	}
	if err != nil {
		return Pair{}, fmt.Errorf("token: denylist: %w", err)
	}
	return j.pair(c.Sub, roles, c.Sid)
}

// Subject is the subject of a refresh token that verifies, for the caller
// to read the user again before Refresh.
func (j *JWT) Subject(ctx context.Context, raw string) (string, error) {
	c, err := j.verify(ctx, raw, typRefresh)
	return c.Sub, err
}

// Revoke ends the session of an access token: logout. Its refresh token ends
// with it.
func (j *JWT) Revoke(ctx context.Context, raw string) error {
	c, err := j.parse(raw, typAccess)
	if err != nil {
		return nil
	}
	return j.deny.Set(ctx, "sid:"+c.Sid, 0, j.clock.Now().Add(j.refresh))
}

// RevokeSubject ends every token subject holds now: a password change, a ban.
// Tokens issued after are not touched.
func (j *JWT) RevokeSubject(ctx context.Context, subject string) error {
	now := j.clock.Now()
	return j.deny.Set(ctx, "sub:"+subject, now.UnixNano(), now.Add(j.refresh))
}

// Purge removes the denylist entries whose tokens have expired anyway.
func (j *JWT) Purge(ctx context.Context) (int64, error) {
	return j.deny.DeleteExpired(ctx, j.clock.Now())
}

// Denylist keeps entries — jti:, sid:, sub: — until they expire.
type Denylist interface {
	// Add puts entry in, or answers ErrDenied when it is there already.
	Add(ctx context.Context, entry string, before int64, expires time.Time) error
	// Set puts entry in, or replaces it.
	Set(ctx context.Context, entry string, before int64, expires time.Time) error
	// Check answers which of the three entries are in, with their before: a
	// token's jti:, sid: and sub:.
	Check(ctx context.Context, jti, sid, sub string) (map[string]int64, error)
	DeleteExpired(ctx context.Context, t time.Time) (int64, error)
}

// ErrDenied is an entry the denylist holds already.
var ErrDenied = errors.New("token: denied already")

//go:embed jwtmigrations
var jwtMigrations embed.FS

// JWTMigrationsTable is where the denylist records its migrations.
const JWTMigrationsTable = "gesso_jwt_migrations"

// MigrateJWT makes the gesso_jwt_denylist table on d, in a history of its own.
func MigrateJWT(ctx context.Context, d *sql.DB, dialect migrate.Dialect) error {
	m, err := migrate.New(d, dialect, jwtMigrations)
	if err != nil {
		return err
	}
	m.Table = JWTMigrationsTable
	_, err = m.Up(ctx)
	return err
}

// DenySQL keeps the denylist in gesso_jwt_denylist.
type DenySQL struct {
	db db.DB
	ph func(int) string
}

// NewDenySQL keeps the denylist in d.
func NewDenySQL(d db.DB, dialect migrate.Dialect) *DenySQL {
	return &DenySQL{db: d, ph: dialect.Placeholder}
}

func (s *DenySQL) has(ctx context.Context, q db.DB, entry string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM gesso_jwt_denylist WHERE entry = "+s.ph(1), entry).Scan(&n)
	return n > 0, err
}

func (s *DenySQL) insert(ctx context.Context, q db.DB, entry string, before int64, expires time.Time) error {
	_, err := q.ExecContext(ctx, "INSERT INTO gesso_jwt_denylist (entry, revoked_before, expires_at) VALUES ("+s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+")",
		entry, before, expires.UnixMilli())
	return err
}

func (s *DenySQL) Add(ctx context.Context, entry string, before int64, expires time.Time) error {
	if in, err := s.has(ctx, s.db, entry); err != nil || in {
		if in {
			return ErrDenied
		}
		return err
	}
	if err := s.insert(ctx, s.db, entry, before, expires); err != nil {
		// Two at once: the one that lost finds the other's row.
		if in, _ := s.has(ctx, s.db, entry); in {
			return ErrDenied
		}
		return err
	}
	return nil
}

func (s *DenySQL) Set(ctx context.Context, entry string, before int64, expires time.Time) error {
	return db.InTx(ctx, s.db, func(tx db.DB) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM gesso_jwt_denylist WHERE entry = "+s.ph(1), entry); err != nil {
			return err
		}
		return s.insert(ctx, tx, entry, before, expires)
	})
}

func (s *DenySQL) Check(ctx context.Context, jti, sid, sub string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT entry, revoked_before FROM gesso_jwt_denylist WHERE entry IN ("+s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+")", jti, sid, sub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]int64{}
	for rows.Next() {
		var entry string
		var before int64
		if err := rows.Scan(&entry, &before); err != nil {
			return nil, err
		}
		found[entry] = before
	}
	return found, rows.Err()
}

func (s *DenySQL) DeleteExpired(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM gesso_jwt_denylist WHERE expires_at <= "+s.ph(1), t.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DenyMemory keeps the denylist in memory, for one process and tests.
type DenyMemory struct {
	mu      sync.Mutex
	entries map[string]denyEntry
}

type denyEntry struct {
	before  int64
	expires time.Time
}

// NewDenyMemory is an empty denylist in memory.
func NewDenyMemory() *DenyMemory { return &DenyMemory{entries: map[string]denyEntry{}} }

func (m *DenyMemory) Add(_ context.Context, entry string, before int64, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[entry]; ok {
		return ErrDenied
	}
	m.entries[entry] = denyEntry{before, expires}
	return nil
}

func (m *DenyMemory) Set(_ context.Context, entry string, before int64, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[entry] = denyEntry{before, expires}
	return nil
}

func (m *DenyMemory) Check(_ context.Context, jti, sid, sub string) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := map[string]int64{}
	for _, e := range []string{jti, sid, sub} {
		if d, ok := m.entries[e]; ok {
			found[e] = d.before
		}
	}
	return found, nil
}

func (m *DenyMemory) DeleteExpired(_ context.Context, t time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for e, d := range m.entries {
		if !d.expires.After(t) {
			delete(m.entries, e)
			n++
		}
	}
	return n, nil
}
