package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var guardName = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

// addGuard is gesso add guard: internal/guards/<name>.go, a gesso.Guard that
// refuses everyone until its check is written.
func addGuard(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("add guard")
	parent := fs.String("dir", "internal", "the folder guards/ goes in")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !guardName.MatchString(pos[0]) {
		return usage("add guard takes one name, lowerCamel: gesso add guard admin")
	}
	name := pos[0]
	p := filepath.Join(dir, *parent, "guards", name+".go")
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("%s already exists", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	src := fmt.Sprintf(`package guards

import (
	"net/http"

	"github.com/Elagoht/gesso"
)

// %[1]s refuses everyone until its check is written, e.g. gesso.RequireRole("admin")(r).
func %[1]s(r *http.Request) error {
	return gesso.ErrForbidden
}
`, exported(name))
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "created", p)
	return nil
}

// guardFor is the Go name of the guard name, which must exist.
func guardFor(dir, parent, name string) (string, error) {
	if !guardName.MatchString(name) {
		return "", fmt.Errorf("a guard name is lowerCamel: %q", name)
	}
	if _, err := os.Stat(filepath.Join(dir, parent, "guards", name+".go")); err != nil {
		return "", fmt.Errorf("no guard %q: run gesso add guard %s first", name, name)
	}
	return exported(name), nil
}

func exported(name string) string { return strings.ToUpper(name[:1]) + name[1:] }
