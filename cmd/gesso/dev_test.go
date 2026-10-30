package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func get(url string) string {
	res, err := http.Get(url)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// gesso dev runs the project with .env, restarts it on a change, keeps it
// through a failed build, and stops it when it is stopped.
func TestDev(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a project")
	}
	root := t.TempDir()
	var out, errOut syncBuffer
	if code := run([]string{"new", "example.com/app"}, root, &out, &errOut, fixedNow); code != 0 {
		t.Fatal(errOut.String())
	}
	dir := filepath.Join(root, "app")
	seedModule(t, dir, "")
	goIn(t, dir, "mod", "tidy")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	// .env.development wins over .env under gesso dev, as gesso.Main reads it.
	env := "# from .env.development\nADDR=" + addr + "\nDATABASE_URL='" + filepath.Join(dir, "dev.db") + "'\n"
	if err := os.WriteFile(filepath.Join(dir, ".env.development"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ADDR=127.0.0.1:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-mod=mod")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- devCommand(ctx, []string{"-poll", "100ms"}, dir, &out, &errOut) }()
	base := "http://" + addr
	waitFor(t, "the app to answer", 60*time.Second, func() bool { return strings.Contains(get(base+"/openapi.json"), `"app"`) })

	api := filepath.Join(dir, "main.go")
	src, _ := os.ReadFile(api)
	if err := os.WriteFile(api, bytes.Replace(src, []byte(`Title: "app"`), []byte(`Title: "changed"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the change to be served", 60*time.Second, func() bool {
		return strings.Contains(get(base+"/openapi.json"), `"changed"`)
	})

	broken := filepath.Join(dir, "broken.go")
	if err := os.WriteFile(broken, []byte("package main\n\nfunc broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed build to be reported", 60*time.Second, func() bool {
		return strings.Contains(errOut.String(), "the build failed")
	})
	// The program is stopped, and its address answers the compiler's output.
	waitFor(t, "the failed build to be answered", 60*time.Second, func() bool {
		body := get(base + "/openapi.json")
		return strings.Contains(body, `"build_failed"`) && strings.Contains(body, "broken.go")
	})
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the fixed build to be served", 60*time.Second, func() bool {
		return strings.Contains(get(base+"/openapi.json"), `"changed"`)
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("dev did not stop")
	}
	if get(base+"/openapi.json") != "" {
		t.Fatal("the app outlived dev")
	}
	if !strings.Contains(out.String(), "changed, rebuilding") {
		t.Fatalf("output:\n%s", out.String())
	}
}
