package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"ID": "id", "UserID": "user_id", "HTTPServer": "http_server", "CreatedAt": "created_at",
		"Title": "title", "URLPath2": "url_path2", "OAuth2Token": "o_auth2_token",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

const genModels = `package notes

import "time"

// Note's Title is the heading column; the rest map by snake_case.
type Note struct {
	ID        int64
	Slug      string
	Title     string ` + "`db:\"heading\"`" + `
	CreatedAt time.Time
	Draft     *bool
	cache     string
}

// Count is handwritten code using the generated Queries: the package must load
// before queries.gen.go exists.
func Count(q *Queries) func() {
	_ = q
	return nil
}
`

func genQueries(dialect string) string {
	p := func(n string) string {
		if dialect == "postgres" {
			return "$" + n
		}
		return "?"
	}
	return `-- gesso: dialect ` + dialect + `

-- name: CreateNote :one
-- in: slug string, title string, at time.Time
-- out: Note
INSERT INTO notes (slug, heading, created_at) VALUES (` + p("1") + `, ` + p("2") + `, ` + p("3") + `)
RETURNING id, slug, heading, created_at, draft;

-- name: NoteBySlug :one
-- in: slug string
-- out: Note
SELECT n.id, n.slug, n.heading, n.created_at, n.draft FROM notes n WHERE n.slug = ` + p("1") + `;

-- name: NotesSince :many
-- in: since time.Time, take int
-- out: Note
SELECT id, slug, heading, created_at, draft FROM notes
WHERE created_at >= ` + p("1") + ` ORDER BY id LIMIT ` + p("2") + `;

-- name: CountNotes :one
-- out: int64
SELECT COUNT(*) AS n FROM notes;

-- name: Rename :execrows
-- in: title string, slug string
UPDATE notes SET heading = ` + p("1") + ` WHERE slug = ` + p("2") + `;

-- name: Clear :exec
DELETE FROM notes;
`
}

func genSchema(dialect string) string {
	id := "id INTEGER PRIMARY KEY"
	ts := "TIMESTAMP"
	if dialect == "postgres" {
		id, ts = "id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY", "TIMESTAMPTZ"
	}
	return "CREATE TABLE notes (" + id + ", slug TEXT NOT NULL UNIQUE, heading TEXT NOT NULL, created_at " + ts + " NOT NULL, draft BOOLEAN)"
}

const genDBTest = `
func exerciseQueries(t *testing.T, d *sql.DB, schema string) {
	ctx := context.Background()
	if _, err := d.Exec(schema); err != nil {
		t.Fatal(err)
	}
	q := NewQueries(d)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	n, err := q.CreateNote(ctx, "jam", "Game jam", at)
	if err != nil || n.ID == 0 || n.Title != "Game jam" || !n.CreatedAt.Equal(at) || n.Draft != nil {
		t.Fatalf("create: %+v %v", n, err)
	}
	if _, err := q.CreateNote(ctx, "talk", "Talk", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := q.NoteBySlug(ctx, "jam")
	if err != nil || got != n {
		t.Fatalf("by slug: %+v %v", got, err)
	}
	if _, err := q.NoteBySlug(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing: %v", err)
	}
	since, err := q.NotesSince(ctx, at.Add(time.Minute), 10)
	if err != nil || len(since) != 1 || since[0].Slug != "talk" {
		t.Fatalf("since: %+v %v", since, err)
	}
	none, err := q.NotesSince(ctx, at.Add(48*time.Hour), 10)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("an empty result is an empty slice, not nil: %#v %v", none, err)
	}
	if c, err := q.CountNotes(ctx); err != nil || c != 2 {
		t.Fatalf("count: %d %v", c, err)
	}
	if r, err := q.Rename(ctx, "Jam!", "jam"); err != nil || r != 1 {
		t.Fatalf("rename: %d %v", r, err)
	}
	// The queries run inside a transaction too.
	if err := db.InTx(ctx, d, func(tx db.DB) error { return NewQueries(tx).Clear(ctx) }); err != nil {
		t.Fatal(err)
	}
	if c, _ := q.CountNotes(ctx); c != 0 {
		t.Fatalf("cleared: %d", c)
	}
}
`

const genSQLiteTest = `package notes

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Elagoht/gesso/db"
	_ "modernc.org/sqlite"
)

func TestQueriesSQLite(t *testing.T) {
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exerciseQueries(t, d, SCHEMA)
}
` + genDBTest

const genPostgresTest = `package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/Elagoht/gesso/db"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestQueriesPostgres(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	dir := t.TempDir()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "rt")).DataPath(filepath.Join(dir, "data")).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		t.Skipf("POSTGRES UNVERIFIED — no server: %v", err)
	}
	defer pg.Stop()
	d, err := sql.Open("pgx", fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exerciseQueries(t, d, SCHEMA)
}
` + genDBTest

// writeFiles puts files under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, src := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGen(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs generated code")
	}
	cases := map[string]struct{ require, dbTest string }{
		"sqlite":   {"require modernc.org/sqlite v1.61.0\n", genSQLiteTest},
		"postgres": {"require (\n\tgithub.com/fergusstrange/embedded-postgres v1.34.0\n\tgithub.com/jackc/pgx/v5 v5.11.0\n)\n", genPostgresTest},
	}
	for dialect, c := range cases {
		t.Run(dialect, func(t *testing.T) {
			dir := newApp(t, c.require)
			writeFiles(t, dir, map[string]string{
				"internal/notes/models.go":   genModels,
				"internal/notes/queries.sql": genQueries(dialect),
				"internal/notes/db_test.go":  strings.Replace(c.dbTest, "SCHEMA", "`"+genSchema(dialect)+"`", 1),
			})
			goIn(t, dir, "mod", "tidy")
			var out, errOut bytes.Buffer
			if code := run([]string{"gen"}, dir, &out, &errOut, fixedNow); code != 0 {
				t.Fatalf("code %d: %s", code, errOut.String())
			}
			src, err := os.ReadFile(filepath.Join(dir, "internal/notes/queries.gen.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(src), genHeader+"\n") || strings.Contains(string(src), "any") {
				t.Fatalf("header or any:\n%s", src)
			}
			goIn(t, dir, "vet", "./...")
			goIn(t, dir, "test", "-count=1", "./...")
			if n, err := check(nil, dir, &out); err != nil || n != 0 {
				t.Fatalf("generated code breaks the ctx rule: %v %s", err, out.String())
			}

			// -check: up to date, then stale after an edit, then missing.
			out.Reset()
			if code := run([]string{"gen", "-check"}, dir, &out, &errOut, fixedNow); code != 0 {
				t.Fatalf("fresh file reported: %d %s %s", code, out.String(), errOut.String())
			}
			sqlPath := filepath.Join(dir, "internal/notes/queries.sql")
			edited := strings.Replace(genQueries(dialect), "ORDER BY id", "ORDER BY created_at", 1)
			writeFiles(t, dir, map[string]string{"internal/notes/queries.sql": edited})
			if code := run([]string{"gen", "-check"}, dir, &out, &errOut, fixedNow); code != 1 || !strings.Contains(out.String(), "stale internal/notes/queries.gen.go") {
				t.Fatalf("stale file not reported: %d %q", code, out.String())
			}
			_ = os.Remove(filepath.Join(dir, "internal/notes/queries.gen.go"))
			if code := run([]string{"gen", "-check"}, dir, &out, &errOut, fixedNow); code != 1 {
				t.Fatalf("missing file not reported: %d", code)
			}
			_ = sqlPath
		})
	}
}

func TestGenRefusesWithALocation(t *testing.T) {
	cases := map[string]struct{ sql, want string }{
		"missing column":   {"-- gesso: dialect postgres\n\n-- name: Bad :one\n-- out: Note\nSELECT id, nope FROM notes", `queries.sql:3: Bad: column "nope" has no field in Note`},
		"two to one field": {"-- gesso: dialect postgres\n-- name: Bad :one\n-- out: Note\nSELECT n.id, n.heading, m.heading FROM notes n JOIN notes m ON m.id = n.id", `columns "heading" and "heading" both land in Note.Title`},
		"placeholders":     {"-- gesso: dialect postgres\n-- name: Bad :exec\n-- in: a string\nDELETE FROM notes WHERE id = $1 AND slug = $2", "the SQL takes 2 parameters, in: lists 1"},
		"star":             {"-- gesso: dialect sqlite\n-- name: Bad :many\n-- out: Note\nSELECT * FROM notes", "name the columns, not *"},
		"no dialect":       {"-- name: Bad :exec\nDELETE FROM notes", "declare the dialect first"},
		"no out":           {"-- gesso: dialect postgres\n-- name: Bad :one\nSELECT id FROM notes", "needs an out: line"},
		"reserved name":    {"-- gesso: dialect postgres\n-- name: Bad :exec\n-- in: ctx string\nDELETE FROM notes WHERE id = $1", `parameter "ctx": the generated code uses that name`},
		"unknown import":   {"-- gesso: dialect postgres\n-- name: Bad :exec\n-- in: id uuid.UUID\nDELETE FROM notes WHERE id = $1", "only time, sql and json"},
		"scalar of two":    {"-- gesso: dialect postgres\n-- name: Bad :one\n-- out: int64\nSELECT id, slug FROM notes", "is not a struct of package notes, and the query returns 2 columns"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newApp(t, "")
			writeFiles(t, dir, map[string]string{"notes/models.go": genModels, "notes/queries.sql": c.sql})
			var out, errOut bytes.Buffer
			if code := run([]string{"gen"}, dir, &out, &errOut, fixedNow); code != 2 || !strings.Contains(errOut.String(), c.want) {
				t.Fatalf("code %d, want %q in:\n%s", code, c.want, errOut.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "notes/queries.gen.go")); err == nil {
				t.Fatal("a refused file is not written")
			}
		})
	}
}
