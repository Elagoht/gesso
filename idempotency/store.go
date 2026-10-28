package idempotency

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"slices"
	"sync"
	"time"

	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
)

// Memory keeps keys in this process: for one replica, and tests. Expired keys
// are dropped as new ones come.
type Memory struct {
	mu   sync.Mutex
	rows map[string]*memRow
}

type memRow struct {
	fingerprint string
	done        bool
	answer      Answer
	lockedUntil time.Time
	expires     time.Time
}

// NewMemory is an empty store.
func NewMemory() *Memory { return &Memory{rows: map[string]*memRow{}} }

// Begin claims id.
func (m *Memory) Begin(_ context.Context, id, fingerprint string, now time.Time, lock, keep time.Duration) (State, Answer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.rows {
		if !r.expires.After(now) {
			delete(m.rows, k)
		}
	}
	r, ok := m.rows[id]
	switch {
	case !ok:
		m.rows[id] = &memRow{fingerprint: fingerprint, lockedUntil: now.Add(lock), expires: now.Add(keep)}
		return Claimed, Answer{}, nil
	case r.fingerprint != fingerprint:
		return Mismatch, Answer{}, nil
	case r.done:
		a := r.answer
		a.Body = slices.Clone(a.Body)
		return Done, a, nil
	case r.lockedUntil.After(now):
		return Running, Answer{}, nil
	}
	r.lockedUntil = now.Add(lock) // the one running stopped answering: take over
	return Claimed, Answer{}, nil
}

// Finish keeps a.
func (m *Memory) Finish(_ context.Context, id string, a Answer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok {
		r.done, r.answer = true, Answer{Status: a.Status, ContentType: a.ContentType, Location: a.Location, Body: slices.Clone(a.Body)}
	}
	return nil
}

// Abandon frees id.
func (m *Memory) Abandon(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok && !r.done {
		delete(m.rows, id)
	}
	return nil
}

//go:embed migrations
var migrations embed.FS

// MigrationsTable is where Migrate records the idempotency migrations.
const MigrationsTable = "gesso_idempotency_migrations"

// Migrations create the gesso_idempotency table in dialect.
func Migrations(dialect migrate.Dialect) (fs.FS, error) {
	dir := map[migrate.Dialect]string{migrate.Postgres: "postgres", migrate.MySQL: "mysql", migrate.SQLite: "sqlite"}[dialect]
	if dir == "" {
		return nil, errors.New("idempotency: no migrations for this dialect: PostgreSQL, MySQL or SQLite")
	}
	return fs.Sub(migrations, "migrations/"+dir)
}

// Migrate creates the table, in a history of its own.
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

// SQL keeps keys in the gesso_idempotency table, which every replica shares.
// Run Purge from a scheduled job to delete expired keys.
type SQL struct {
	db      db.DB
	dialect migrate.Dialect
}

// NewSQL keeps keys in d.
func NewSQL(d db.DB, dialect migrate.Dialect) *SQL { return &SQL{db: d, dialect: dialect} }

func (s *SQL) ph(n int) string { return s.dialect.Placeholder(n) }

// Begin claims id: an insert that only one request wins, else a look at what
// is there, and a take-over of a lock that ran out, which again only one wins.
func (s *SQL) Begin(ctx context.Context, id, fingerprint string, now time.Time, lock, keep time.Duration) (State, Answer, error) {
	n := now.UnixNano()
	for attempt := 0; attempt < 2; attempt++ {
		insert := `INSERT INTO gesso_idempotency (id, fingerprint, state, status, content_type, location, body, locked_until, expires_at)
			VALUES (` + s.ph(1) + `, ` + s.ph(2) + `, 'running', 0, '', '', ` + s.ph(3) + `, ` + s.ph(4) + `, ` + s.ph(5) + `)`
		if s.dialect == migrate.MySQL {
			insert = "INSERT IGNORE" + insert[len("INSERT"):]
		} else {
			insert += ` ON CONFLICT (id) DO NOTHING`
		}
		res, err := s.db.ExecContext(ctx, insert, id, fingerprint, []byte{}, now.Add(lock).UnixNano(), now.Add(keep).UnixNano())
		if err != nil {
			return 0, Answer{}, err
		}
		if got, err := res.RowsAffected(); err != nil || got == 1 {
			return Claimed, Answer{}, err
		}
		var fp, state string
		var a Answer
		var locked, expires int64
		err = s.db.QueryRowContext(ctx, `SELECT fingerprint, state, status, content_type, location, body, locked_until, expires_at
			FROM gesso_idempotency WHERE id = `+s.ph(1), id).Scan(&fp, &state, &a.Status, &a.ContentType, &a.Location, &a.Body, &locked, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			continue // deleted between the two: insert again
		}
		if err != nil {
			return 0, Answer{}, err
		}
		if expires <= n {
			// Kept long enough: gone, and the key free again.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM gesso_idempotency WHERE id = `+s.ph(1)+` AND expires_at <= `+s.ph(2), id, n); err != nil {
				return 0, Answer{}, err
			}
			continue
		}
		switch {
		case fp != fingerprint:
			return Mismatch, Answer{}, nil
		case state == "done":
			return Done, a, nil
		case locked > n:
			return Running, Answer{}, nil
		}
		res, err = s.db.ExecContext(ctx, `UPDATE gesso_idempotency SET locked_until = `+s.ph(1)+`
			WHERE id = `+s.ph(2)+` AND state = 'running' AND locked_until <= `+s.ph(3), now.Add(lock).UnixNano(), id, n)
		if err != nil {
			return 0, Answer{}, err
		}
		if got, err := res.RowsAffected(); err != nil || got == 1 {
			return Claimed, Answer{}, err
		}
		return Running, Answer{}, nil // another took it over first
	}
	return Running, Answer{}, nil
}

// Finish keeps a.
func (s *SQL) Finish(ctx context.Context, id string, a Answer) error {
	_, err := s.db.ExecContext(ctx, `UPDATE gesso_idempotency SET state = 'done', status = `+s.ph(1)+`,
		content_type = `+s.ph(2)+`, location = `+s.ph(3)+`, body = `+s.ph(4)+` WHERE id = `+s.ph(5),
		a.Status, a.ContentType, a.Location, append([]byte{}, a.Body...), id)
	return err
}

// Abandon frees id.
func (s *SQL) Abandon(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM gesso_idempotency WHERE id = `+s.ph(1)+` AND state = 'running'`, id)
	return err
}

// Purge deletes keys kept past their time.
func (s *SQL) Purge(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM gesso_idempotency WHERE expires_at <= `+s.ph(1), now.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
