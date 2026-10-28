package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var migrationName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

// parseFlags reads flags wherever they stand among the positional arguments:
// "add module events -fields x" and "add module -fields x events" both work.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func migrateNew(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("migrate new", flag.ContinueOnError)
	target := fs.String("dir", "migrations", "the migrations folder")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("migrate new takes one name: gesso migrate new create_events")
	}
	if !migrationName.MatchString(pos[0]) {
		return fmt.Errorf("a migration name is lower case letters, digits and underscores: %q", pos[0])
	}
	paths, err := writeMigration(filepath.Join(dir, *target), pos[0], "", "", now)
	if err != nil {
		return err
	}
	for _, p := range paths {
		fmt.Fprintln(stdout, "created", p)
	}
	return nil
}

// writeMigration creates the up and down files of a new version, refusing to
// overwrite either.
func writeMigration(folder, name, up, down string, now func() time.Time) ([]string, error) {
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, err
	}
	version := now().UTC().Format("20060102150405")
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
