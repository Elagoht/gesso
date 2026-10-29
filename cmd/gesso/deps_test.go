package main

// The modules the generated test code imports — SQLite, an embedded PostgreSQL
// and the MySQL driver, and what gesso/auth does — are this module's test dependencies, so their checksums are in
// go.sum. newApp seeds every temporary module with that go.sum: its go mod tidy
// then finds every hash there and never asks the checksum database, whose
// hiccups would otherwise fail these tests at random.
import (
	_ "github.com/fergusstrange/embedded-postgres"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/term"
	_ "modernc.org/sqlite"
)
