package gesso

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
)

func echoApp(env Env) (*App, error) {
	a, err := New(Config{Logger: env.Logger, Addr: env.Addr})
	if err != nil {
		return nil, err
	}
	a.Command("echo", func(_ context.Context, args []string, out io.Writer) error {
		fmt.Fprintf(out, "%s|%s|%s|%s", os.Getenv("GREETING"), os.Getenv("SHELL_WINS"), env.Addr, strings.Join(args, ","))
		return nil
	})
	return a, nil
}

func TestMainReadsTheEnvFile(t *testing.T) {
	t.Chdir(t.TempDir())
	_ = os.WriteFile(".env", []byte("GREETING=prod\nSHELL_WINS=file\nADDR=:9000 # a comment\n"), 0o600)
	_ = os.WriteFile(".env.development", []byte("export GREETING='dev'\n"), 0o600)
	t.Setenv("SHELL_WINS", "shell")
	for _, k := range []string{"GREETING", "ADDR"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("GESSO_DEV", "")
	var out, errOut bytes.Buffer
	if code := runMain(echoApp, []string{"echo", "a", "b"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if out.String() != "prod|shell|:9000|a,b" || !strings.Contains(errOut.String(), "loaded .env") {
		t.Fatalf("a built binary reads .env, the shell winning: %q %q", out.String(), errOut.String())
	}

	os.Unsetenv("GREETING")
	os.Unsetenv("ADDR")
	t.Setenv("GESSO_DEV", "1")
	out.Reset()
	if code := runMain(echoApp, []string{"-addr", "127.0.0.1:7", "echo"}, &out, io.Discard); code != 0 {
		t.Fatal(code)
	}
	if out.String() != "dev|shell|127.0.0.1:7|" {
		t.Fatalf("under gesso dev .env.development, alone: %q", out.String())
	}
}

func TestMainCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	var errOut bytes.Buffer
	if code := runMain(echoApp, []string{"sideways"}, io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), `no command "sideways": the app has echo`) {
		t.Fatalf("%d %s", code, errOut.String())
	}
	_ = os.WriteFile(".env", []byte("NOT A PAIR\n"), 0o600)
	errOut.Reset()
	if code := runMain(echoApp, []string{"echo"}, io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), ".env:1") {
		t.Fatalf("a bad line: %d %s", code, errOut.String())
	}
}

// gesso-inspect prints the routes and commands as JSON, and serves nothing.
func TestMainInspect(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	if code := runMain(echoApp, []string{inspectCommand}, &out, &errOut); code != 0 {
		t.Fatalf("%d %s", code, errOut.String())
	}
	var got Inspection
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got.Version != 1 || !slices.Equal(got.Commands, []string{"echo"}) || got.Routes == nil {
		t.Fatalf("%+v", got)
	}
}
