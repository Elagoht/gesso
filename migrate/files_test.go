package migrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewReadsAndOrdersAcrossSources(t *testing.T) {
	users := fstest.MapFS{
		"migrations/20261028100000_users.up.sql":   {Data: []byte("CREATE TABLE users (id INT);")},
		"migrations/20261028100000_users.down.sql": {Data: []byte("DROP TABLE users;")},
		"migrations/README.md":                     {Data: []byte("not a migration")},
	}
	events := fstest.MapFS{
		"20261027090000_events.up.sql": {Data: []byte("-- gesso:no-transaction\nCREATE INDEX x;")},
	}
	m, err := New(nil, SQLite, users, events)
	if err != nil {
		t.Fatal(err)
	}
	got := m.Migrations()
	if len(got) != 2 || got[0].Name != "events" || got[1].Name != "users" {
		t.Fatalf("got %+v", got)
	}
	if !got[0].NoTransaction || got[1].NoTransaction || got[0].Down != "" || got[1].Down == "" || len(got[1].Checksum) != 64 {
		t.Fatalf("got %+v", got)
	}
}

func TestNewRejectsBadSets(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"ends in .up.sql":           {"1_a.sql": {}},
		"begins with its version":   {"abc_a.up.sql": {}},
		"is both":                   {"1_a.up.sql": {Data: []byte("x")}, "1_b.down.sql": {Data: []byte("y")}},
		"has a down file but no up": {"1_a.down.sql": {Data: []byte("y")}},
	}
	for want, fsys := range cases {
		if _, err := New(nil, SQLite, fsys); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}
