package token

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
)

// Migrations create the table SQLStore uses. Apply them with Migrate, not beside
// the application's own: their versions are fixed in gesso, so in the
// application's history they would sort before migrations it already ran.
//
//go:embed migrations
var Migrations embed.FS

// MigrationsTable is where Migrate records the token migrations: a history of
// their own, apart from the application's.
const MigrationsTable = "gesso_token_migrations"

// Migrate applies the token migrations to d.
func Migrate(ctx context.Context, d *sql.DB, dialect migrate.Dialect) error {
	m, err := migrate.New(d, dialect, Migrations)
	if err != nil {
		return err
	}
	m.Table = MigrationsTable
	_, err = m.Up(ctx)
	return err
}

// SQLStore keeps records in the gesso_tokens table.
type SQLStore struct {
	db db.DB
	ph func(int) string
}

// NewSQLStore keeps records in d, writing parameters as dialect does.
func NewSQLStore(d db.DB, dialect migrate.Dialect) *SQLStore {
	return &SQLStore{db: d, ph: dialect.Placeholder}
}

// Save stores r under hash.
func (s *SQLStore) Save(ctx context.Context, hash string, r Record) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO gesso_tokens (hash, subject, roles, created_at, expires_at) VALUES ("+
			s.ph(1)+", "+s.ph(2)+", "+s.ph(3)+", "+s.ph(4)+", "+s.ph(5)+")",
		hash, r.Subject, strings.Join(r.Roles, ","), r.CreatedAt.UnixMilli(), r.ExpiresAt.UnixMilli())
	return err
}

// Find returns the record under hash.
func (s *SQLStore) Find(ctx context.Context, hash string) (Record, error) {
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
func (s *SQLStore) Delete(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM gesso_tokens WHERE hash = "+s.ph(1), hash)
	return err
}

// DeleteSubject removes every record of subject.
func (s *SQLStore) DeleteSubject(ctx context.Context, subject string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM gesso_tokens WHERE subject = "+s.ph(1), subject)
	return err
}

// DeleteExpired removes the records expired at or before t.
func (s *SQLStore) DeleteExpired(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM gesso_tokens WHERE expires_at <= "+s.ph(1), t.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MemoryStore keeps records in memory: for tests, and for a single process that
// may forget its sessions on restart.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

// NewMemoryStore is an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{records: map[string]Record{}} }

// Save stores r under hash.
func (m *MemoryStore) Save(_ context.Context, hash string, r Record) error {
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
func (m *MemoryStore) Find(_ context.Context, hash string) (Record, error) {
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
func (m *MemoryStore) Delete(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, hash)
	return nil
}

// DeleteSubject removes every record of subject.
func (m *MemoryStore) DeleteSubject(_ context.Context, subject string) error {
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
func (m *MemoryStore) DeleteExpired(_ context.Context, t time.Time) (int64, error) {
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
