package main

import (
	"slices"
	"strings"
	"testing"
)

func TestPlaceholders(t *testing.T) {
	cases := []struct {
		dialect, sql string
		want         int
		err          string
	}{
		{"postgres", "SELECT * FROM t WHERE a = $1 AND b = $2", 2, ""},
		{"postgres", "SELECT * FROM t WHERE a = $1 OR b = $1", 1, ""},
		{"postgres", "SELECT data ? 'key', '$9', data::jsonb FROM t WHERE id = $1 -- $5\n/* $7 */", 1, ""},
		{"postgres", "DO $body$ SELECT $3 $body$; SELECT $1", 1, ""},
		{"postgres", "SELECT $$ it's $2 $$ , $1", 1, ""},
		{"postgres", "SELECT * FROM t WHERE a = $2", 0, "$1 is never used"},
		{"sqlite", "SELECT * FROM t WHERE a = ? AND b = '?' AND c = \"?\" -- ?\n AND d = ?", 2, ""},
		{"mysql", "SELECT `a?` FROM t WHERE x = 'it''s ?' AND y = ?", 1, ""},
		{"postgres", "SELECT 'unclosed", 0, "unclosed string"},
		{"postgres", "SELECT (1", 0, "( with no )"},
	}
	for _, c := range cases {
		toks, err := scanSQL(c.sql, c.dialect)
		var n int
		if err == nil {
			n, err = placeholders(toks, c.dialect)
		}
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: want error %q, got %v", c.sql, c.err, err)
			}
			continue
		}
		if err != nil || n != c.want {
			t.Errorf("%q: got %d %v, want %d", c.sql, n, err, c.want)
		}
	}
}

func TestResultColumns(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
		err  string
	}{
		{"SELECT id, n.slug, \"Title\" FROM notes n", []string{"id", "slug", "title"}, ""},
		{"SELECT DISTINCT COUNT(*) AS total, coalesce(a, b) AS ab FROM t", []string{"total", "ab"}, ""},
		{"SELECT f(x, y) AS z, CAST(a AS text) AS a_text FROM t", []string{"z", "a_text"}, ""},
		{"WITH recent AS (SELECT id FROM t) SELECT r.id, 'x' AS kind FROM recent r", []string{"id", "kind"}, ""},
		{"INSERT INTO t (a, b) VALUES ($1, $2) RETURNING id, created_at", []string{"id", "created_at"}, ""},
		{"INSERT INTO t (a) SELECT a FROM u RETURNING id", []string{"id"}, ""},
		{"SELECT a::text AS a FROM t;", []string{"a"}, ""},
		{"SELECT * FROM t", nil, "name the columns, not *"},
		{"SELECT t.* FROM t", nil, "name the columns, not *"},
		{"SELECT COUNT(*) FROM t", nil, "give the expression a name with AS"},
		{"DELETE FROM t WHERE id = $1", nil, "no SELECT or RETURNING"},
	}
	for _, c := range cases {
		toks, err := scanSQL(c.sql, "postgres")
		if err != nil {
			t.Fatalf("%q: %v", c.sql, err)
		}
		got, err := resultColumns(toks)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: want error %q, got %v %v", c.sql, c.err, got, err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q: got %v %v, want %v", c.sql, got, err, c.want)
		}
	}
}
