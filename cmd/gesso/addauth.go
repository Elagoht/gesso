package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	gotoken "go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"
)

// addAuth is gesso add auth: the users who sign in, as a module of the app's —
// internal/users, its model //gesso:auth with the fields given, managed by
// admins, each user changing the fields marked self on themselves — and the
// signedIn and admin guards. Sign-in itself is gesso/auth's: login, logout,
// the tokens, the users command.
func addAuth(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := newFlags("add auth")
	parent := fs.String("dir", "internal", "the folder users/ and guards/ go in")
	tokens := fs.String("tokens", "session", "session: opaque tokens kept in a table; jwt: an access and a refresh JSON Web Token, a denylist kept")
	ttl := fs.Duration("ttl", 30*24*time.Hour, "how long a session lasts")
	access := fs.Duration("access", 15*time.Minute, "how long a JWT access token lasts")
	refresh := fs.Duration("refresh", 30*24*time.Hour, "how long a JWT refresh token lasts")
	var fieldList fieldFlags
	fs.Var(&fieldList, "field", "a user's field beside username, name:type[:options]: avatar:image:self,public,fill=256x256; again for each")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("add auth takes no arguments, only flags")
	}
	directive := "session ttl=" + shortDuration(*ttl)
	switch *tokens {
	case "session":
	case "jwt":
		if *refresh <= *access {
			return usage("-refresh outlives -access")
		}
		directive = "jwt access=" + shortDuration(*access) + " refresh=" + shortDuration(*refresh)
	default:
		return usage("-tokens is session or jwt")
	}
	for name, src := range map[string]string{"signedIn": signedInGuard, "admin": adminGuard} {
		guard := filepath.Join(dir, *parent, "guards", name+".go")
		if _, err := os.Stat(guard); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(guard), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(guard, []byte(src), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", guard)
	}
	moduleArgs := []string{"users", "-auth=" + directive, "-entity", "User", "-guard", "admin", "-read", "admin", "-dir", *parent,
		"-field", "username:string:key,lower,max=100"}
	for _, f := range fieldList {
		moduleArgs = append(moduleArgs, "-field", f)
	}
	if err := addModule(moduleArgs, dir, stdout, now); err != nil {
		return err
	}
	if *tokens == "jwt" {
		if err := jwtSecret(dir, stdout); err != nil {
			return err
		}
	}
	fmt.Fprint(stdout, `
next:
  go run . users add <username> -role admin
  gesso add module <name> -guard signedIn -field <field>:<type>
`)
	return nil
}

const signedInGuard = `package guards

import (
	"net/http"

	"github.com/Elagoht/gesso"
)

func SignedIn(r *http.Request) error {
	return gesso.RequireIdentity(r)
}
`

const adminGuard = `package guards

import (
	"net/http"

	"github.com/Elagoht/gesso"
)

func Admin(r *http.Request) error {
	return gesso.RequireRole("admin")(r)
}
`

// insert puts text at at, in place of the cut bytes there.
type insert struct {
	at, cut int
	text    string
}

// applyInserts applies inserts, the later offset first so the earlier stay right.
func applyInserts(src []byte, inserts []insert) []byte {
	sort.SliceStable(inserts, func(i, j int) bool { return inserts[i].at > inserts[j].at })
	out := bytes.Clone(src)
	for _, in := range inserts {
		out = append(out[:in.at:in.at], append([]byte(in.text), out[in.at+in.cut:]...)...)
	}
	return out
}

// reformat parses src again, adds imports and formats it.
func reformat(fset *gotoken.FileSet, src []byte, paths ...string) ([]byte, error) {
	f, err := parser.ParseFile(fset, "register.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("wiring broke it: %w", err)
	}
	for _, p := range paths {
		astutil.AddImport(fset, f, p)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	// The project's own imports in a group of their own, after the others.
	return imports.Process("", buf.Bytes(), &imports.Options{Comments: true, TabIndent: true, TabWidth: 8, FormatOnly: true})
}

func isName(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// shortDuration is d as a directive writes it: 720h, 15m, 90s.
func shortDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return d.String()
}

// jwtSecret gives development a JWT_SECRET of its own, in .env.development,
// which git ignores, and .env.example a line saying one is needed.
func jwtSecret(dir string, stdout io.Writer) error {
	var b [48]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	dev := filepath.Join(dir, ".env.development")
	data, _ := os.ReadFile(dev)
	if !strings.Contains(string(data), "JWT_SECRET=") {
		if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
			data = append(data, '\n')
		}
		data = append(data, "JWT_SECRET="+base64.RawURLEncoding.EncodeToString(b[:])+"\n"...)
		if err := os.WriteFile(dev, data, 0o600); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "wrote JWT_SECRET to", dev)
	}
	example := filepath.Join(dir, ".env.example")
	if data, err := os.ReadFile(example); err == nil && !strings.Contains(string(data), "JWT_SECRET") {
		data = append(data, "# signs the JWTs: 32 random bytes or more, openssl rand -base64 48\nJWT_SECRET=\n"...)
		if err := os.WriteFile(example, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
