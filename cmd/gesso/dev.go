package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// devCommand is gesso dev: build the program in dir, run it in development —
// GESSO_DEV=1, so gesso.Main reads .env.development, or .env — and rebuild and
// restart it whenever a .go, .sql, go.mod or environment file changes, until
// ctx ends. A build that fails leaves the running program as it was, and says
// why.
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
	build := func() bool {
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, *pkg)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &out
		if err := cmd.Run(); err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(stderr, "gesso dev: the build failed; the running version stays:\n%s", out.String())
			}
			return false
		}
		return true
	}
	defer stop()

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
