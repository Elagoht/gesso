package ratelimit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"time"

	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
)

//go:embed migrations
var migrations embed.FS

// MigrationsTable is where Migrate records the rate limit's migrations.
const MigrationsTable = "gesso_ratelimit_migrations"

// Migrations create the gesso_ratelimit table in dialect.
func Migrations(dialect migrate.Dialect) (fs.FS, error) {
	dir := map[migrate.Dialect]string{migrate.Postgres: "postgres", migrate.MySQL: "mysql", migrate.SQLite: "sqlite"}[dialect]
	if dir == "" {
		return nil, errors.New("ratelimit: no migrations for this dialect: PostgreSQL, MySQL or SQLite")
	}
	return fs.Sub(migrations, "migrations/"+dir)
}

// Migrate creates the gesso_ratelimit table, in a history of its own.
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

// SQL keeps the clients in the gesso_ratelimit table, which every replica
// shares: a client gets Burst requests across all of them, not per replica.
//
// A Take is one UPDATE that moves the client's arrival time on only if the
// request fits; the database makes two at once take turns. A client seen for
// the first time costs an INSERT more. Times are Unix nanoseconds, from the
// caller's clock: the replicas' clocks must agree to well within an interval.
//
// Rows of clients whose bucket is full again are of no use; Purge deletes them —
// run it from a scheduled job.
type SQL struct {
	db      db.DB
	dialect migrate.Dialect
}

// NewSQL keeps the clients in d, written as dialect does.
func NewSQL(d db.DB, dialect migrate.Dialect) *SQL { return &SQL{db: d, dialect: dialect} }

// Take takes a token.
func (s *SQL) Take(ctx context.Context, key string, now time.Time, interval time.Duration, burst int) (bool, time.Duration, error) {
	key = column(key)
	n, t, limit := now.UnixNano(), interval.Nanoseconds(), int64(burst)*interval.Nanoseconds()
	for attempt := 0; ; attempt++ {
		res, err := s.update(ctx, key, n, t, limit)
		if err != nil {
			return false, 0, err
		}
		if got, err := res.RowsAffected(); err != nil || got == 1 {
			return err == nil, 0, err
		}
		var tat int64
		err = s.db.QueryRowContext(ctx, `SELECT tat FROM gesso_ratelimit WHERE client = `+s.dialect.Placeholder(1), key).Scan(&tat)
		if errors.Is(err, sql.ErrNoRows) && attempt == 0 {
			// A new client, with a full bucket. Another replica may insert it
			// first; either way the UPDATE now finds it.
			if _, err := s.db.ExecContext(ctx, s.insert(), key, n); err != nil {
				return false, 0, err
			}
			continue
		}
		if err != nil {
			return false, 0, err
		}
		_, _, wait := gcra(time.Unix(0, tat), now, interval, burst)
		return false, wait, nil
	}
}

func (s *SQL) update(ctx context.Context, key string, n, t, limit int64) (sql.Result, error) {
	switch s.dialect {
	case migrate.Postgres:
		return s.db.ExecContext(ctx, `UPDATE gesso_ratelimit SET tat = GREATEST(tat, $2) + $3
			WHERE client = $1 AND GREATEST(tat, $2) + $3 - $2 <= $4`, key, n, t, limit)
	case migrate.MySQL:
		return s.db.ExecContext(ctx, `UPDATE gesso_ratelimit SET tat = GREATEST(tat, ?) + ?
			WHERE client = ? AND GREATEST(tat, ?) + ? - ? <= ?`, n, t, key, n, t, n, limit)
	default: // SQLite: MAX of two values is its GREATEST
		return s.db.ExecContext(ctx, `UPDATE gesso_ratelimit SET tat = MAX(tat, ?) + ?
			WHERE client = ? AND MAX(tat, ?) + ? - ? <= ?`, n, t, key, n, t, n, limit)
	}
}

func (s *SQL) insert() string {
	switch s.dialect {
	case migrate.Postgres:
		return `INSERT INTO gesso_ratelimit (client, tat) VALUES ($1, $2) ON CONFLICT (client) DO NOTHING`
	case migrate.MySQL:
		return `INSERT IGNORE INTO gesso_ratelimit (client, tat) VALUES (?, ?)`
	default:
		return `INSERT INTO gesso_ratelimit (client, tat) VALUES (?, ?) ON CONFLICT (client) DO NOTHING`
	}
}

// Purge deletes the clients whose bucket is full again at now: they would be
// let in as if never seen.
func (s *SQL) Purge(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM gesso_ratelimit WHERE tat <= `+s.dialect.Placeholder(1), now.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// column is key as the table keeps it: a key longer than the column is hashed.
func column(key string) string {
	if len(key) <= 255 {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}
