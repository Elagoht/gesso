package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		{"int64", "image"},
		{"string", "image,accept=image/svg+xml"},
		{"string", "image,key"},
		{"string", "file,fit=10x10"},
		{"string", "image,fit=wide"},
		{"string", "image,format=webp"},
		{"string", "image,maxsize=lots"},
		{"string", "public"},
		{"int64", "trim"},
		{"string", "file,trim"},
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

func TestFileField(t *testing.T) {
	f, err := newMField("Cover", "string", "image,fill=256x256,maxsize=5MiB,public")
	if err != nil || f.File != "image" || !f.Fill || f.Width != 256 || f.MaxBytes != 5<<20 || !f.Public || f.ResType() != "*string" ||
		f.AcceptTag() != "image/png,image/jpeg,image/gif,image/webp" || f.Base != "file" {
		t.Fatalf("%+v %v", f, err)
	}
	f, _ = newMField("Name", "string", "trim,collapse,slugify")
	if got := f.TransformExpr("v"); got != "text.Slugify(text.Collapse(strings.TrimSpace(v)))" {
		t.Fatalf("transforms: %s", got)
	}
}

func TestAuthModelRefuses(t *testing.T) {
	for name, src := range map[string]string{
		"no username":   "//gesso:table users\n//gesso:auth\ntype User struct {\n\tID int64\n\tEmail string `gesso:\"key\"`\n}\n",
		"self alone":    "//gesso:table notes\ntype Note struct {\n\tID int64\n\tTitle string `gesso:\"self\"`\n}\n",
		"own column":    "//gesso:table users\n//gesso:auth\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n\tRoles string\n}\n",
		"unique":        "//gesso:table users\n//gesso:auth\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n\tEmail string `gesso:\"unique\"`\n}\n",
		"gate":          "//gesso:table users\n//gesso:auth\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n\tActive bool `gesso:\"gate=admin\"`\n}\n",
		"guarded file":  "//gesso:table docs\ntype Doc struct {\n\tID int64\n\tSecret string `gesso:\"file,guard=admin\"`\n}\n",
		"read file":     "//gesso:table docs\n//gesso:read admin\ntype Doc struct {\n\tID int64\n\tCover string `gesso:\"image\"`\n}\n",
		"bad tokens":    "//gesso:table users\n//gesso:auth oauth\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n}\n",
		"short refresh": "//gesso:table users\n//gesso:auth jwt access=1h refresh=30m\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n}\n",
		"ttl on jwt":    "//gesso:table users\n//gesso:auth jwt ttl=1h\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n}\n",
		"bad duration":  "//gesso:table users\n//gesso:auth session ttl=forever\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n}\n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "models.go"), []byte("package models\n\n"+src), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := parseModel(dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAuthTokens(t *testing.T) {
	for words, want := range map[string]string{
		"":                            "session 720h0m0s",
		" session ttl=12h":            "session 12h0m0s",
		" jwt":                        "jwt 15m0s 720h0m0s",
		" jwt access=5m refresh=168h": "jwt 5m0s 168h0m0s",
	} {
		dir := t.TempDir()
		src := "package models\n\n//gesso:table users\n//gesso:auth" + words + "\ntype User struct {\n\tID int64\n\tUsername string `gesso:\"key\"`\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "models.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := parseModel(dir)
		if err != nil {
			t.Fatalf("%q: %v", words, err)
		}
		got := "session " + m.TTL.String()
		if m.JWT {
			got = "jwt " + m.Access.String() + " " + m.Refresh.String()
		}
		if got != want {
			t.Errorf("%q: %s, want %s", words, got, want)
		}
	}
	if got := DurationExpr(90 * time.Minute); got != "90 * time.Minute" {
		t.Errorf("DurationExpr: %s", got)
	}
}
