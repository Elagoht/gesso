package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var lowerCamelName = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

// addCommand is gesso add command <name>: internal/commands/<name>.go, run as
// "app <name>", added in register.go.
func addCommand(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("add command")
	parent := fs.String("dir", "internal", "the folder commands/ goes in")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !lowerCamelName.MatchString(pos[0]) {
		return usage("add command takes one name, lowerCamel: gesso add command seed")
	}
	name := pos[0]
	src := fmt.Sprintf(`package commands

import (
	"context"
	"fmt"
	"io"

	"github.com/Elagoht/gesso"
)

// %[1]s runs as: app %[2]s [args]
func %[1]s(app *gesso.App) func(ctx context.Context, args []string, out io.Writer) error {
	return func(ctx context.Context, args []string, out io.Writer) error {
		fmt.Fprintln(out, "%[2]s: nothing yet")
		return nil
	}
}
`, exported(name), name)
	return addWired(dir, *parent, "commands", name, src, func(app string) string {
		return fmt.Sprintf("%s.Command(%q, commands.%s(%s))", app, name, exported(name), app)
	}, stdout)
}

// addMiddleware is gesso add middleware <name>: internal/middleware/<name>.go,
// around every route, added in register.go.
func addMiddleware(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("add middleware")
	parent := fs.String("dir", "internal", "the folder middleware/ goes in")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !lowerCamelName.MatchString(pos[0]) {
		return usage("add middleware takes one name, lowerCamel: gesso add middleware loadUser")
	}
	name := pos[0]
	src := fmt.Sprintf(`package middleware

import "net/http"

// %[1]s runs around every route; it passes each request on until you write it.
func %[1]s(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
`, exported(name))
	return addWired(dir, *parent, "middleware", name, src, func(app string) string {
		return app + ".Use(middleware." + exported(name) + ")"
	}, stdout)
}

// addWired writes <parent>/<folder>/<name>.go and adds stmt to register.go,
// before it registers the modules, importing the folder.
func addWired(dir, parent, folder, name, src string, stmt func(app string) string, stdout io.Writer) error {
	p := filepath.Join(dir, parent, folder, name+".go")
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("%s already exists", p)
	}
	regGo, reg, err := registerFile(dir)
	if err != nil {
		return err
	}
	mod, err := readModule(dir)
	if err != nil {
		return err
	}
	wired, err := wireStatement(reg, stmt, mod+"/"+filepath.ToSlash(filepath.Join(parent, folder)))
	if err != nil {
		return fmt.Errorf("%s: %w", regGo, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "created", p)
	if err := os.WriteFile(regGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", regGo)
	return nil
}
