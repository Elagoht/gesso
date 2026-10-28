package token

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
)

//go:embed migrations
var migrations embed.FS

// Migrations create the table SQL uses; one set serves every dialect. Apply
// them with Migrate, not beside the application's own: their versions are fixed
// in gesso, so in the application's history they would sort before migrations
// it already ran.
func Migrations(migrate.Dialect) (fs.FS, error) { return fs.Sub(migrations, "migrations") }

// MigrationsTable is where Migrate records the token migrations: a history of
// their own, apart from the application's.
const MigrationsTable = "gesso_token_migrations"

// Migrate applies the token migrations to d.
func Migrate(ctx context.Context, d *sql.DB, dialect migrate.Dialect) error {
	files, err := Migrations(dialect)
	if err != nil {
		return err
	}
	m, err := migrate.New(d, dialect, files)
	if err != nil {
		return err
	}
	m.Table = MigrationsTable
	_, err = m.Up(ctx)
	return err
}

// SQL keeps records in the gesso_tokens table.
type SQL struct {
	db db.DB
	ph func(int) string
}

// NewSQL keeps records in d, writing parameters as dialect does.
func NewSQL(d db.DB, dialect migrate.Dialect) *SQL {
	return &SQL{db: d, ph: dialect.Placeholder}
}

// Save stores r under hash.
func (s *SQL) Save(ctx context.Context, hash string, r Record) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO gesso_tokens (hash, subject, roles, created_at, expires_at) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+")",
		hash, r.Subject, strings.Join(r.Roles, ","), r.CreatedAt.UnixMilli(), r.ExpiresAt.UnixMilli())
	return err
}

// Find returns the record under hash.
func (s *SQL) Find(ctx context.Context, hash string) (Record, error) {
	var r Record
	var roles string
	var created, expires int64
	err := s.db.QueryRowContext(ctx,
		"SELECT subject, roles, created_at, expires_at FROM gesso_tokens WHERE hash = "+s.ph(1), hash).
		Scan(&r.Subject, &roles, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	if roles != "" {
		r.Roles = strings.Split(roles, ",")
	}
	r.CreatedAt = time.UnixMilli(created).UTC()
	r.ExpiresAt = time.UnixMilli(expires).UTC()
	return r, nil
}

// Delete removes hash.
func (s *SQL) Delete(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM gesso_tokens WHERE hash = "+s.ph(1), hash)
	return err
}

// DeleteSubject removes every record of subject.
func (s *SQL) DeleteSubject(ctx context.Context, subject string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM gesso_tokens WHERE subject = "+s.ph(1), subject)
	return err
}

// DeleteExpired removes the records expired at or before t.
func (s *SQL) DeleteExpired(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM gesso_tokens WHERE expires_at <= "+s.ph(1), t.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Memory keeps records in memory: for tests, and for a single process that
// may forget its sessions on restart.
type Memory struct {
	mu      sync.Mutex
	records map[string]Record
}

// NewMemory is an empty store.
func NewMemory() *Memory { return &Memory{records: map[string]Record{}} }

// Save stores r under hash.
func (m *Memory) Save(_ context.Context, hash string, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.records[hash]; ok {
		return errors.New("token: hash already stored")
	}
	r.Roles = slices.Clone(r.Roles)
	m.records[hash] = r
	return nil
}

// Find returns the record under hash.
func (m *Memory) Find(_ context.Context, hash string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[hash]
	if !ok {
		return Record{}, ErrNotFound
	}
	r.Roles = slices.Clone(r.Roles)
	return r, nil
}

// Delete removes hash.
func (m *Memory) Delete(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, hash)
	return nil
}

// DeleteSubject removes every record of subject.
func (m *Memory) DeleteSubject(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, r := range m.records {
		if r.Subject == subject {
			delete(m.records, h)
		}
	}
	return nil
}

// DeleteExpired removes the records expired at or before t.
func (m *Memory) DeleteExpired(_ context.Context, t time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for h, r := range m.records {
		if !r.ExpiresAt.After(t) {
			delete(m.records, h)
			n++
		}
	}
	return n, nil
}
