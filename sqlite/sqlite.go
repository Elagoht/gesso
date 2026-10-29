// Package sqlite is a gesso.Database on a SQLite file, through modernc.org/sqlite
// (no cgo): foreign keys on, WAL, a five-second busy timeout.
package sqlite

import (
	"database/sql"
	"errors"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
	"modernc.org/sqlite"
)

// Open is the database in the file at path.
func Open(path string) gesso.Database { return database{path: path} }

type database struct{ path string }

func (d database) Open() (*sql.DB, error) {
	if d.path == "" {
		return nil, errors.New("sqlite: no file")
	}
	return sql.Open("sqlite", "file:"+d.path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
}

func (d database) OpenMigrator() (*sql.DB, error) { return d.Open() }

func (database) Dialect() migrate.Dialect { return migrate.SQLite }

func (database) Errors(fields map[string]string) gesso.ErrorMapper {
	return db.Errors(classify, fields)
}

func classify(err error) (db.Violation, bool) {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return db.Violation{}, false
	}
	return db.SQLiteViolation(se.Code(), se.Error())
}
