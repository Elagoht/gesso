package auth

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/gesso/clock"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
	"github.com/Elagoht/gesso/password"
	"github.com/Elagoht/gesso/token"
)

//go:embed migrations
var migrations embed.FS

// MigrationsTable is where SQL records its migrations: a history of their own,
// apart from the application's, since their versions are fixed in gesso.
const MigrationsTable = "gesso_auth_migrations"

// Migrations create the gesso_users table in dialect.
func Migrations(dialect migrate.Dialect) (fs.FS, error) {
	dir := map[migrate.Dialect]string{migrate.Postgres: "postgres", migrate.MySQL: "mysql", migrate.SQLite: "sqlite"}[dialect]
	if dir == "" {
		return nil, errors.New("auth: no migrations for this dialect: PostgreSQL, MySQL or SQLite")
	}
	return fs.Sub(migrations, "migrations/"+dir)
}

// SQL keeps the users in the gesso_users table: a username, matched exactly —
// fold it before, for names that should not differ by case — an argon2id hash
// of the password, and roles. A user's ID is their row's id.
//
// It is a PasswordChanger. Migrate makes its table; its Command manages the
// users from the command line.
type SQL struct {
	db      *sql.DB
	dialect migrate.Dialect
	ph      func(int) string
	// Hasher hashes the passwords. Nil: password.New(), argon2id at OWASP's
	// parameters. A hash made with older parameters is made again at the
	// user's next sign-in.
	Hasher password.Hasher
	// Clock tells created_at. Nil: the system clock.
	Clock clock.Clock
	// Table is where the users are. Empty: gesso_users. Another is the app's
	// own — a //gesso:auth model's, with columns of its own beside these —
	// whose migrations make it.
	Table string

	dummyOnce sync.Once
	dummy     string // a hash nobody's password matches, for a missing user
}

// NewSQL keeps the users in d, written as dialect does.
func NewSQL(d *sql.DB, dialect migrate.Dialect) *SQL {
	return &SQL{db: d, dialect: dialect, ph: dialect.Placeholder}
}

// Migrate makes the tables of the module and of NewSQL — gesso_tokens and
// gesso_users — on d, each in a history of its own. On MySQL, d takes several
// statements in one Exec (multiStatements=true), as every migration
// connection there does.
func Migrate(ctx context.Context, d *sql.DB, dialect migrate.Dialect) error {
	if err := token.Migrate(ctx, d, dialect); err != nil {
		return fmt.Errorf("auth: tokens: %w", err)
	}
	return migrateUsers(ctx, d, dialect)
}

func migrateUsers(ctx context.Context, d *sql.DB, dialect migrate.Dialect) error {
	files, err := Migrations(dialect)
	if err != nil {
		return err
	}
	m, err := migrate.New(d, dialect, files)
	if err != nil {
		return err
	}
	m.Table = MigrationsTable
	if _, err = m.Up(ctx); err != nil {
		return fmt.Errorf("auth: users: %w", err)
	}
	return nil
}

func (s *SQL) hasher() password.Hasher {
	if s.Hasher == nil {
		return password.New()
	}
	return s.Hasher
}

const userColumns = "id, username, password_hash, roles"

type row struct {
	User
	n    int64 // the id, as the table keeps it
	hash string
}

func (s *SQL) scan(r *sql.Row) (row, error) {
	var out row
	var roles string
	if err := r.Scan(&out.n, &out.Username, &out.hash, &roles); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return row{}, ErrNoUser
		}
		return row{}, err
	}
	out.ID = strconv.FormatInt(out.n, 10)
	out.Roles = splitRoles(roles)
	return out, nil
}

func (s *SQL) byUsername(ctx context.Context, q db.DB, username string) (row, error) {
	return s.scan(q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM "+s.table()+" WHERE username = "+s.ph(1), username))
}

func (s *SQL) byID(ctx context.Context, id string) (row, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return row{}, ErrNoUser
	}
	return s.scan(s.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM "+s.table()+" WHERE id = "+s.ph(1), n))
}

// Authenticate verifies password against the user's hash; for a username
// nobody has, against a hash of its own, so that the answer takes as long.
func (s *SQL) Authenticate(ctx context.Context, username, pass string) (User, error) {
	r, err := s.byUsername(ctx, s.db, username)
	if errors.Is(err, ErrNoUser) {
		s.dummyOnce.Do(func() { s.dummy, _ = s.hasher().Hash("gesso: no user has this password") })
		_, _ = s.hasher().Verify(pass, s.dummy)
		return User{}, ErrBadCredentials
	}
	if err != nil {
		return User{}, err
	}
	ok, err := s.hasher().Verify(pass, r.hash)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, ErrBadCredentials
	}
	if s.hasher().NeedsRehash(r.hash) {
		// Best effort: the sign-in stands if the new hash is not written.
		if h, err := s.hasher().Hash(pass); err == nil {
			_, _ = s.db.ExecContext(ctx, "UPDATE "+s.table()+" SET password_hash = "+s.ph(1)+" WHERE id = "+s.ph(2)+" AND password_hash = "+s.ph(3), h, r.n, r.hash)
		}
	}
	return r.User, nil
}

// Lookup returns the user with id.
func (s *SQL) Lookup(ctx context.Context, id string) (User, error) {
	r, err := s.byID(ctx, id)
	return r.User, err
}

// Create adds a user: ErrUsernameTaken when the name is someone's,
// ErrWeakPassword when the password is shorter than MinPassword.
func (s *SQL) Create(ctx context.Context, username, pass string, roles ...string) (User, error) {
	if username == "" || len(username) > 255 {
		return User{}, errors.New("auth: a username is 1 to 255 bytes")
	}
	if err := checkRoles(roles); err != nil {
		return User{}, err
	}
	hash, err := s.hash(pass)
	if err != nil {
		return User{}, err
	}
	u := User{Username: username, Roles: slices.Clone(roles)}
	joined, created := strings.Join(roles, ","), s.now().UnixMilli()
	insert := "INSERT INTO " + s.table() + " (username, password_hash, roles, created_at) VALUES (" + s.ph(1) + ", " + s.ph(2) + ", " + s.ph(3) + ", " + s.ph(4) + ")"
	var id int64
	if s.dialect == migrate.MySQL {
		var res sql.Result
		if res, err = s.db.ExecContext(ctx, insert, username, hash, joined, created); err == nil {
			id, err = res.LastInsertId()
		}
	} else {
		err = s.db.QueryRowContext(ctx, insert+" RETURNING id", username, hash, joined, created).Scan(&id)
	}
	if err != nil {
		// The unique index refused it, unless something else did: the error
		// is the driver's, so the row is looked for instead of the error read.
		if _, lerr := s.byUsername(ctx, s.db, username); lerr == nil {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	u.ID = strconv.FormatInt(id, 10)
	return u, nil
}

// ChangePassword sets next if current is the user's password.
func (s *SQL) ChangePassword(ctx context.Context, id, current, next string) error {
	r, err := s.byID(ctx, id)
	if err != nil {
		return err
	}
	ok, err := s.hasher().Verify(current, r.hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadCredentials
	}
	return s.setPassword(ctx, r.n, next)
}

// SetPassword sets the password of username, without the old one: an
// operator's reset. Their tokens are revoked.
func (s *SQL) SetPassword(ctx context.Context, username, pass string) error {
	r, err := s.byUsername(ctx, s.db, username)
	if err != nil {
		return err
	}
	if err := s.setPassword(ctx, r.n, pass); err != nil {
		return err
	}
	return s.revoke(ctx, r.ID)
}

func (s *SQL) setPassword(ctx context.Context, id int64, pass string) error {
	hash, err := s.hash(pass)
	if err != nil {
		return err
	}
	return s.one(s.db.ExecContext(ctx, "UPDATE "+s.table()+" SET password_hash = "+s.ph(1)+" WHERE id = "+s.ph(2), hash, id))
}

// SetRoles replaces the roles of username. Their tokens are revoked: a token
// holds the roles it was issued with.
func (s *SQL) SetRoles(ctx context.Context, username string, roles ...string) error {
	if err := checkRoles(roles); err != nil {
		return err
	}
	r, err := s.byUsername(ctx, s.db, username)
	if err != nil {
		return err
	}
	if err := s.one(s.db.ExecContext(ctx, "UPDATE "+s.table()+" SET roles = "+s.ph(1)+" WHERE id = "+s.ph(2), strings.Join(roles, ","), r.n)); err != nil {
		return err
	}
	return s.revoke(ctx, r.ID)
}

// Delete removes username, and their tokens.
func (s *SQL) Delete(ctx context.Context, username string) error {
	r, err := s.byUsername(ctx, s.db, username)
	if err != nil {
		return err
	}
	if err := s.one(s.db.ExecContext(ctx, "DELETE FROM "+s.table()+" WHERE id = "+s.ph(1), r.n)); err != nil {
		return err
	}
	return s.revoke(ctx, r.ID)
}

// List returns every user, by username.
func (s *SQL) List(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, username, roles FROM "+s.table()+" ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var id int64
		var u User
		var roles string
		if err := rows.Scan(&id, &u.Username, &roles); err != nil {
			return nil, err
		}
		u.ID, u.Roles = strconv.FormatInt(id, 10), splitRoles(roles)
		out = append(out, u)
	}
	return out, rows.Err()
}

// revoke deletes the user's tokens, which the module keeps in the same
// database: gesso_tokens, when the module has made it.
func (s *SQL) revoke(ctx context.Context, id string) error {
	err := token.NewSQL(s.db, s.dialect).DeleteSubject(ctx, id)
	if err != nil && !s.tokensExist(ctx) {
		return nil // the module keeps its tokens elsewhere
	}
	return err
}

func (s *SQL) tokensExist(ctx context.Context) bool {
	var n int
	return s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM gesso_tokens WHERE 1 = 0").Scan(&n) == nil
}

func (s *SQL) table() string {
	if s.Table == "" {
		return "gesso_users"
	}
	return s.Table
}

// Hash is pass as the users' table keeps it, refusing a password shorter than
// MinPassword.
func (s *SQL) Hash(pass string) (string, error) { return s.hash(pass) }

func (s *SQL) hash(pass string) (string, error) {
	if len([]rune(pass)) < MinPassword {
		return "", ErrWeakPassword
	}
	return s.hasher().Hash(pass)
}

func (s *SQL) one(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	return ErrNoUser
}

func (s *SQL) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

func checkRoles(roles []string) error {
	for _, r := range roles {
		if r == "" || strings.ContainsAny(r, ",\n") {
			return fmt.Errorf("auth: role %q is empty or holds a comma", r)
		}
	}
	return nil
}

func splitRoles(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}
