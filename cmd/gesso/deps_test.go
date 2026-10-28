package main

// The modules the generated test code imports — SQLite and an embedded
// PostgreSQL — are this module's test dependencies, so their checksums are in
// go.sum. newApp seeds every temporary module with that go.sum: its go mod tidy
// then finds every hash there and never asks the checksum database, whose
// hiccups would otherwise fail these tests at random.
import (
	_ "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)
