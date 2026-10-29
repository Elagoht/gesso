// Package postgres is a gesso.Database on PostgreSQL, through pgx.
package postgres

import (
	"database/sql"
	"errors"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Open is the database at url: postgres://user:pass@host:5432/name?sslmode=disable.
func Open(url string) gesso.Database { return database{url: url} }

type database struct{ url string }

func (d database) Open() (*sql.DB, error) {
	if d.url == "" {
		return nil, errors.New("postgres: no URL")
	}
	return sql.Open("pgx", d.url)
}

func (d database) OpenMigrator() (*sql.DB, error) { return d.Open() }

func (database) Dialect() migrate.Dialect { return migrate.Postgres }

func (database) Errors(fields map[string]string) gesso.ErrorMapper {
	return db.Errors(classify, fields)
}

func classify(err error) (db.Violation, bool) {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return db.Violation{}, false
	}
	return db.PostgresViolation(pe.Code, pe.ConstraintName)
}
