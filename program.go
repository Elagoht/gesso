package gesso

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Env is what Main knows about the run before the app exists: what "gesso dev"
// and the environment asked for. Main hands it to the function that builds the
// app; a test builds one by hand.
type Env struct {
	// Dev is development mode: GESSO_DEV=1, which "gesso dev" sets.
	Dev bool
	// Addr is where to listen: -addr, ADDR, or ":8080".
	Addr string
	// BasePath is BASE_PATH: "/api" when collage serves the app under /api/.
	BasePath string
	// Logger writes text to standard error in development, JSON to standard
	// output otherwise.
	Logger *slog.Logger
}

// Main runs the program "gesso new" scaffolds, around newApp, which builds the
// app from env:
//
//	func main() { gesso.Main(newApp) }
//
// Before newApp runs it loads the working directory's environment file —
// .env.development, or .env, in development; .env otherwise — leaving alone
// what the environment already sets, so os.Getenv in newApp reads it with no
// `source .env` first, under "gesso dev", "go run" and a built binary alike.
//
// With no arguments the app serves until SIGINT or SIGTERM (App.Run). With a
// word after the flags Main runs the app's Command of that name instead —
// "migrate status" — without starting it, shuts it down, and exits with the
// command's result.
func Main(newApp func(Env) (*App, error)) {
	os.Exit(runMain(newApp, os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(newApp func(Env) (*App, error), args []string, stdout, stderr io.Writer) int {
	name := filepath.Base(os.Args[0])
	dev := os.Getenv("GESSO_DEV") == "1"
	loaded, err := LoadEnvFile(".", dev)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if loaded != "" && !dev {
		fmt.Fprintf(stderr, "%s: loaded %s\n", name, loaded)
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	addr := flags.String("addr", envOr("ADDR", ":8080"), "where to listen (env ADDR)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	env := Env{Dev: dev, Addr: *addr, BasePath: os.Getenv("BASE_PATH"), Logger: slog.New(slog.NewJSONHandler(stdout, nil))}
	if terminal, color := onTerminal(stderr); terminal {
		env.Logger = slog.New(newTerminalHandler(stderr, color))
	} else if dev {
		env.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	}
	app, err := newApp(env)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if flags.NArg() == 0 {
		if err := app.Run(); err != nil {
			env.Logger.Error(name, "error", err)
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := 0
	run := app.runCommand
	if flags.Arg(0) == inspectCommand {
		run = app.inspect
	}
	if err := run(ctx, flags.Arg(0), flags.Args()[1:], stdout); err != nil {
		fmt.Fprintf(stderr, "%s %s: %v\n", name, flags.Arg(0), err)
		code = 1
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Shutdown(sctx); err != nil {
		fmt.Fprintf(stderr, "%s: shutdown: %v\n", name, err)
		code = 1
	}
	return code
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Command adds a command Main runs, by name, instead of serving: "app migrate
// status" runs the "migrate" command with ["status"]. The app is built but not
// started; out is standard output.
func (a *App) Command(name string, run func(ctx context.Context, args []string, out io.Writer) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.commands == nil {
		a.commands = map[string]func(context.Context, []string, io.Writer) error{}
	}
	a.commands[name] = run
}

func (a *App) runCommand(ctx context.Context, name string, args []string, out io.Writer) error {
	a.mu.Lock()
	run, ok := a.commands[name]
	names := make([]string, 0, len(a.commands))
	for n := range a.commands {
		names = append(names, n)
	}
	a.mu.Unlock()
	if !ok {
		slices.Sort(names)
		if len(names) == 0 {
			return fmt.Errorf("no command %q: the app has none", name)
		}
		return fmt.Errorf("no command %q: the app has %s", name, strings.Join(names, ", "))
	}
	if a.db != nil && name != "migrate" {
		// A command works on the app's tables as they are now: users add on a
		// new database makes them first.
		if err := a.migrateUp(ctx); err != nil {
			return err
		}
	}
	return run(ctx, args, out)
}

// envFiles are the files LoadEnvFile reads, by mode. Only the first found is.
var envFiles = map[bool][]string{true: {".env.development", ".env"}, false: {".env"}}

// LoadEnvFile sets the variables of dir's environment file — .env, or in
// development .env.development, and .env when there is none — that the
// environment does not set already: a variable in the environment wins over
// the file. It returns the name of the file it read, or "". Main calls it; a
// program of its own making may too.
//
// The file is KEY=value lines, blank lines and "#" comments, an optional
// "export ", and optional matching quotes around a value, kept as written. An
// unquoted value ends at a " #". A line that is none of these is an error, not
// a setting silently lost.
func LoadEnvFile(dir string, dev bool) (string, error) {
	for _, name := range envFiles[dev] {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		pairs, err := parseEnv(name, f)
		_ = f.Close()
		if err != nil {
			return "", err
		}
		for _, p := range pairs {
			if _, set := os.LookupEnv(p[0]); set {
				continue
			}
			if err := os.Setenv(p[0], p[1]); err != nil {
				return "", err
			}
		}
		return name, nil
	}
	return "", nil
}

func parseEnv(name string, r io.Reader) ([][2]string, error) {
	var pairs [][2]string
	sc := bufio.NewScanner(r)
	for number := 1; sc.Scan(); number++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fail := func(reason string) error { return fmt.Errorf("%s:%d: %s", name, number, reason) }
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fail("want KEY=value")
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return nil, fail(fmt.Sprintf("%q is not a variable name", key))
		}
		value = strings.TrimSpace(value)
		if value != "" && (value[0] == '"' || value[0] == '\'') {
			end := strings.IndexByte(value[1:], value[0])
			if end < 0 {
				return nil, fail("unterminated quote")
			}
			if rest := strings.TrimSpace(value[end+2:]); rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, fail("text after the closing quote")
			}
			value = value[1 : end+1]
		} else if i := strings.IndexAny(value, " \t"); i >= 0 {
			if j := strings.Index(value[i:], "#"); j >= 0 && strings.TrimSpace(value[i:i+j]) == "" {
				value = strings.TrimSpace(value[:i])
			}
		}
		pairs = append(pairs, [2]string{key, value})
	}
	return pairs, sc.Err()
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// inspectCommand is the word gesso routes, gesso inspect and gesso check run
// a program with: it prints the app's routes and commands as JSON, and serves
// nothing.
const inspectCommand = "gesso-inspect"

// Inspection is what gesso-inspect prints.
type Inspection struct {
	Version  int         `json:"version"`
	Routes   []RouteInfo `json:"routes"`
	Commands []string    `json:"commands"`
}

func (a *App) inspect(_ context.Context, _ string, _ []string, out io.Writer) error {
	a.mu.Lock()
	names := make([]string, 0, len(a.commands))
	for n := range a.commands {
		names = append(names, n)
	}
	a.mu.Unlock()
	slices.Sort(names)
	routes := a.Routes()
	if routes == nil {
		routes = []RouteInfo{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(Inspection{Version: 1, Routes: routes, Commands: names})
}
