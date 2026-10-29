// Package mysql is a gesso.Database on MySQL, through go-sql-driver/mysql. The
// app's pool runs one statement per Exec; migrations get a connection of their
// own that runs several.
package mysql

import (
	"database/sql"
	"errors"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/migrate"
	"github.com/go-sql-driver/mysql"
)

// Open is the database at dsn: user:pass@tcp(host:3306)/name.
func Open(dsn string) gesso.Database { return database{dsn: dsn} }

type database struct{ dsn string }

func (d database) Open() (*sql.DB, error) { return d.open(false) }

func (d database) OpenMigrator() (*sql.DB, error) { return d.open(true) }

func (d database) open(multi bool) (*sql.DB, error) {
	if d.dsn == "" {
		return nil, errors.New("mysql: no DSN")
	}
	cfg, err := mysql.ParseDSN(d.dsn)
	if err != nil {
		return nil, err
	}
	cfg.MultiStatements, cfg.ParseTime = multi, true
	c, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(c), nil
}

func (database) Dialect() migrate.Dialect { return migrate.MySQL }

func (database) Errors(fields map[string]string) gesso.ErrorMapper {
	return db.Errors(classify, fields)
}

func classify(err error) (db.Violation, bool) {
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		return db.Violation{}, false
	}
	return db.MySQLViolation(me.Number, me.Message)
}
