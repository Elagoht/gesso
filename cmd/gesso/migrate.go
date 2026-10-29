package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var migrationName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

func migrateNew(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := newFlags("migrate new")
	target := fs.String("dir", "migrations", "the migrations folder")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("migrate new takes one name: gesso migrate new create_events")
	}
	if !migrationName.MatchString(pos[0]) {
		return usage(fmt.Sprintf("a migration name is lower case letters, digits and underscores: %q", pos[0]))
	}
	paths, err := writeMigration(dir, filepath.Join(dir, *target), pos[0], "", "", now)
	if err != nil {
		return err
	}
	for _, p := range paths {
		fmt.Fprintln(stdout, "created", p)
	}
	return nil
}

// writeMigration creates the up and down files of a new version, refusing to
// overwrite either. The version is now, or the first second after it that no
// migration under root has: every module's migrations are applied as one
// history, and two scaffolded in the same second would collide.
func writeMigration(root, folder, name, up, down string, now func() time.Time) ([]string, error) {
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, err
	}
	if existing, _ := filepath.Glob(filepath.Join(folder, "*_"+name+".up.sql")); len(existing) > 0 {
		return nil, fmt.Errorf("a migration named %s exists already: %s", name, existing[0])
	}
	taken := usedVersions(root)
	at := now().UTC()
	version := at.Format("20060102150405")
	for taken[version] {
		at = at.Add(time.Second)
		version = at.Format("20060102150405")
	}
	var paths []string
	for _, f := range []struct{ dir, body string }{{"up", up}, {"down", down}} {
		p := filepath.Join(folder, version+"_"+name+"."+f.dir+".sql")
		file, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return paths, err
		}
		_, werr := io.WriteString(file, f.body)
		if err := errors.Join(werr, file.Close()); err != nil {
			return paths, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

var versionPrefix = regexp.MustCompile(`^(\d{14})_.*\.(up|down)\.sql$`)

// usedVersions are the migration versions in the files under root, skipping
// hidden folders and vendor.
func usedVersions(root string) map[string]bool {
	taken := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if m := versionPrefix.FindStringSubmatch(d.Name()); m != nil {
			taken[m[1]] = true
		}
		return nil
	})
	return taken
}
