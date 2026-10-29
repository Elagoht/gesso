package main

import (
	"strings"
	"testing"
)

func TestMigrationDiff(t *testing.T) {
	col := func(name, sql, base string) column { return column{Name: name, SQL: sql, Base: base} }
	old := schemaSnapshot{Table: "docs", Dialect: "mysql", Columns: []column{col("title", "VARCHAR(100)", "string"), col("body", "TEXT", "text")}}

	wider := old
	wider.Columns = []column{col("title", "VARCHAR(200)", "string"), col("body", "TEXT", "text")}
	if _, up, down, err := migration(&old, wider, false); err != nil || !strings.Contains(up, "MODIFY COLUMN title VARCHAR(200) NOT NULL") || !strings.Contains(down, "VARCHAR(100)") {
		t.Fatalf("a wider VARCHAR on MySQL: %q %q %v", up, down, err)
	}

	gone := old
	gone.Columns = []column{col("title", "VARCHAR(100)", "string")}
	if _, _, _, err := migration(&old, gone, false); err == nil || !strings.Contains(err.Error(), "-drop") {
		t.Fatalf("a column gone without -drop: %v", err)
	}
	if name, up, down, err := migration(&old, gone, true); err != nil || name != "docs_drop_body" || up != "ALTER TABLE docs DROP COLUMN body;\n" || !strings.Contains(down, "ADD COLUMN body TEXT NOT NULL DEFAULT ('')") {
		t.Fatalf("drop: %s %q %q %v", name, up, down, err)
	}

	retyped := old
	retyped.Columns = []column{col("title", "BIGINT", "int"), col("body", "TEXT", "text")}
	if _, _, _, err := migration(&old, retyped, true); err == nil || !strings.Contains(err.Error(), "-no-migration") {
		t.Fatalf("a type change: %v", err)
	}

	unique := old
	unique.Columns = []column{{Name: "title", SQL: "VARCHAR(100)", Base: "string", Unique: true}, col("body", "TEXT", "text")}
	if _, up, down, err := migration(&old, unique, false); err != nil || up != "CREATE UNIQUE INDEX docs_title_unique ON docs (title);\n" || down != "DROP INDEX docs_title_unique ON docs;\n" {
		t.Fatalf("unique: %q %q %v", up, down, err)
	}

	if name, up, _, err := migration(&old, old, false); err != nil || name != "" || up != "" {
		t.Fatalf("no change: %q %q %v", name, up, err)
	}
	moved := old
	moved.Table = "pages"
	if _, _, _, err := migration(&old, moved, false); err == nil {
		t.Fatal("a model moved to another table")
	}
}

func TestModelRefuses(t *testing.T) {
	for typ, tag := range map[string]string{
		"string":   "unknownrule",
		"int64":    "text",
		"*string":  "key",
		"bool":     "sort",
		"[]string": "",
	} {
		if _, err := newMField("Field", typ, tag); err == nil {
			t.Errorf("%s `gesso:%q` is accepted", typ, tag)
		}
	}
	if _, err := newMField("Body", "string", "text,unique"); err == nil {
		t.Error("a unique text column")
	}
	f, err := newMField("Title", "string", "optional,max=80,email")
	if err != nil || f.Rule != "max=80,email" || f.Patch != "notnull,max=80,email" {
		t.Fatalf("%+v %v", f, err)
	}
	f, _ = newMField("Name", "string", "")
	if f.Rule != "required,max=255" {
		t.Fatalf("a plain string: %q", f.Rule)
	}
	for _, c := range []struct{ typ, tag string }{
		{"string", "key,default=x"},
		{"string", "unique,default=x"},
		{"string", "default=@today"},
		{"*date.Date", "default=@user"},
		{"time.Time", "default=@today"},
		{"string", "default=@tomorrow"},
		{"int64", "default=many"},
		{"bool", "default=maybe"},
		{"*date.Date", "default=2026-01-01"},
		{"string", "oneof=draft live,default=gone"},
		{"string", "default="},
		{"time.Time", "optional"},
	} {
		if _, err := newMField("Field", c.typ, c.tag); err == nil {
			t.Errorf("%s `gesso:%q` is accepted", c.typ, c.tag)
		}
	}
	for _, c := range []struct{ typ, tag, value string }{
		{"string", "oneof=draft live,default=draft", `"draft"`},
		{"int64", "default=5", "5"},
		{"*date.Date", "default=@today", "date.Today(time.Now(), time.Local)"},
		{"time.Time", "default=@now", "time.Now()"},
		{"string", "default=@user", "id.Name"},
	} {
		f, err := newMField("Field", c.typ, c.tag)
		if err != nil || !f.Optional || f.DefaultValue() != c.value || f.ReqType() != "gesso.Optional["+c.typ+"]" {
			t.Errorf("%s `gesso:%q`: %+v %v", c.typ, c.tag, f, err)
		}
	}
}
