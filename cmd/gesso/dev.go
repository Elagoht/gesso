package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// devCommand is gesso dev: build the program in dir, run it in development —
// GESSO_DEV=1, so gesso.Main reads .env.development, or .env — and rebuild and
// restart it whenever a .go, .sql, go.mod or environment file changes, until
// ctx ends. A build that fails stops the program: until the next build that
// works, every request to its address is answered with the compiler's output,
// a 500 build_failed problem, and the terminal says it too.
func devCommand(ctx context.Context, args []string, dir string, stdout, stderr io.Writer) error {
	fs := newFlags("dev")
	every := fs.Duration("poll", 500*time.Millisecond, "how often files are looked at")
	pkg := fs.String("pkg", ".", "the package to build and run")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{withSuggestion(err, fs)}
	}
	programArgs := fs.Args() // what follows the flags (after --) goes to the program
	tmp, err := os.MkdirTemp("", "gesso-dev-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "app")

	var proc *exec.Cmd
	var exited chan struct{}
	start := func() {
		proc = exec.Command(bin, programArgs...)
		proc.Dir, proc.Env, proc.Stdout, proc.Stderr = dir, append(os.Environ(), "GESSO_DEV=1"), stdout, stderr
		if err := proc.Start(); err != nil {
			fmt.Fprintln(stderr, "gesso dev: start:", err)
			proc = nil
			return
		}
		exited = make(chan struct{})
		p, done := proc, exited
		go func() { _ = p.Wait(); close(done) }()
	}
	stop := func() {
		if proc == nil {
			return
		}
		// SIGTERM first: the program drains and closes its database, as in
		// production. Five seconds, then it is killed.
		_ = proc.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = proc.Process.Kill()
			<-exited
		}
		proc = nil
	}
	addr := devAddr(dir, programArgs)
	var failed *http.Server
	build := func() bool {
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, *pkg)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &out
		if err := cmd.Run(); err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(stderr, "gesso dev: the build failed:\n%s", out.String())
				stop()
				if failed == nil {
					failed = serveBuildError(addr, stderr)
				}
				buildError.Store(out.String())
			}
			return false
		}
		if failed != nil {
			_ = failed.Close()
			failed = nil
		}
		return true
	}
	defer func() {
		stop()
		if failed != nil {
			_ = failed.Close()
		}
	}()

	fmt.Fprintln(stdout, "gesso dev: building")
	seen := snapshot(dir)
	if build() {
		start()
	}
	tick := time.NewTicker(*every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		now := snapshot(dir)
		changed := changedFile(seen, now)
		if changed == "" {
			continue
		}
		seen = now
		fmt.Fprintf(stdout, "gesso dev: %s changed, rebuilding\n", changed)
		if build() {
			stop()
			start()
		}
	}
}

// buildError is the output of the build that failed, which the address answers.
var buildError atomic.Value

// serveBuildError answers every request on addr with the failed build's output
// as a problem, until it is closed.
func serveBuildError(addr string, stderr io.Writer) *http.Server {
	srv := &http.Server{Addr: addr, ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, _ := buildError.Load().(string)
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(buildProblem{
			Type: "about:blank", Title: "Internal Server Error", Status: http.StatusInternalServerError,
			Code: "build_failed", Detail: strings.TrimSpace(out), Instance: r.URL.Path,
		})
	})}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(stderr, "gesso dev: answer the failed build:", err)
		return srv
	}
	go func() { _ = srv.Serve(l) }()
	return srv
}

type buildProblem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}

// devAddr is where the program listens, as gesso.Main finds it: -addr after
// the flags, ADDR in the environment, then in .env.development or .env, then
// :8080.
func devAddr(dir string, args []string) string {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "-addr="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "--addr="); ok {
			return v
		}
		if (a == "-addr" || a == "--addr") && i+1 < len(args) {
			return args[i+1]
		}
	}
	if v := os.Getenv("ADDR"); v != "" {
		return v
	}
	for _, name := range []string{".env.development", ".env"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(data)) {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
			if v, ok := strings.CutPrefix(line, "ADDR="); ok {
				return strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
		break
	}
	return ":8080"
}

// snapshot is the modification time of every file dev watches under dir.
func snapshot(dir string) map[string]time.Time {
	files := map[string]time.Time{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".sql") || name == "go.mod" || name == ".env" || name == ".env.development" {
			if info, err := d.Info(); err == nil {
				files[p] = info.ModTime()
			}
		}
		return nil
	})
	return files
}

// changedFile names a file added, removed or changed between two snapshots,
// or "".
func changedFile(before, after map[string]time.Time) string {
	for p, t := range after {
		if old, ok := before[p]; !ok || !old.Equal(t) {
			return filepath.Base(p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			return filepath.Base(p)
		}
	}
	return ""
}
