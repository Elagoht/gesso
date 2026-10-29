package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpAndUsage(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		args     []string
		code     int
		out, err string
	}{
		{nil, 2, "", "usage: gesso <command>"},
		{[]string{"help"}, 0, "add endpoint", ""},
		{[]string{"--help"}, 0, "gesso help <command>", ""},
		{[]string{"help", "add", "module"}, 0, "-field", ""},
		{[]string{"add", "module", "-h"}, 0, "usage: gesso add module <name> [flags]", ""},
		{[]string{"chek"}, 2, "", `did you mean "check"?`},
		{[]string{"add", "modul", "x"}, 2, "", `did you mean "add module"?`},
		{[]string{"add"}, 2, "", "add what?"},
		{[]string{"add", "module", "x", "-fild", "a:string"}, 2, "", "did you mean -field?"},
		{[]string{"new", "x", "-template", "dmeo"}, 2, "", `did you mean "demo"?`},
		{[]string{"migrate", "new"}, 2, "", "takes one name"},
		{[]string{"version"}, 0, "gesso ", ""},
		{[]string{"-v"}, 0, "gesso ", ""},
		{[]string{"completion", "tcsh"}, 2, "", "bash, zsh or fish"},
	} {
		var out, errOut bytes.Buffer
		code := run(c.args, dir, &out, &errOut, fixedNow)
		if code != c.code || !strings.Contains(out.String(), c.out) || !strings.Contains(errOut.String(), c.err) {
			t.Errorf("%v: %d\nout: %s\nerr: %s", c.args, code, out.String(), errOut.String())
		}
	}
}

func TestCompletionAndMan(t *testing.T) {
	scripts := map[string]string{}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var out, errOut bytes.Buffer
		if code := run([]string{"completion", shell}, t.TempDir(), &out, &errOut, fixedNow); code != 0 {
			t.Fatalf("%s: %s", shell, errOut.String())
		}
		scripts[shell] = out.String()
	}
	if !strings.Contains(scripts["bash"], `"add module") flags=`) || !strings.Contains(scripts["bash"], "-field") {
		t.Fatalf("bash:\n%s", scripts["bash"])
	}
	if !strings.Contains(scripts["fish"], "-a endpoint") || !strings.Contains(scripts["fish"], "-o guard") {
		t.Fatalf("fish:\n%s", scripts["fish"])
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(scripts["bash"])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
		}
	}
	var man bytes.Buffer
	if code := run([]string{"man"}, t.TempDir(), &man, &man, fixedNow); code != 0 || !strings.HasPrefix(man.String(), ".TH GESSO 1") || !strings.Contains(man.String(), "gesso add endpoint") {
		t.Fatalf("man:\n%s", man.String())
	}
}

func TestAIInit(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"ai", "init"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatal(errOut.String())
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(string(agents), agentsBegin) || !strings.Contains(string(agents), "## Project rules") {
		t.Fatalf("%s", agents)
	}
	if c, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(c) != "@AGENTS.md\n" {
		t.Fatalf("CLAUDE.md: %q", c)
	}
	mine := strings.Replace(string(agents), "<!-- Your own rules", "Always write tests.\n<!-- Your own rules", 1)
	mine = strings.Replace(mine, "## gesso\n", "## gesso (old)\n", 1)
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(mine), 0o644)
	if code := run([]string{"ai", "init"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatal(errOut.String())
	}
	again, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if strings.Contains(string(again), "(old)") || !strings.Contains(string(again), "Always write tests.") {
		t.Fatalf("refreshed:\n%s", again)
	}
	for name, text := range map[string]string{
		"no markers": "# Mine\n",
		"two begins": agentsBegin + " -->\n" + agentsBegin + " -->\n" + agentsEnd + "\n",
		"no end":     agentsBegin + " -->\nx\n",
	} {
		merged, err := mergeAgents([]byte(text), true, "SECTION\n")
		if name == "no markers" {
			if err != nil || merged != "SECTION\n\n# Mine\n" {
				t.Errorf("%s: %q %v", name, merged, err)
			}
		} else if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A demo project, then an endpoint, a command and a middleware added to it:
// it builds, passes its tests, lists its routes, checks clean, and its
// binary runs the command.
func TestDemoProject(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a project")
	}
	root := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"new", "example.com/app", "-template", "demo", "-ai"}, root, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("new: %s", errOut.String())
	}
	dir := filepath.Join(root, "app")
	seedModule(t, dir, "require modernc.org/sqlite v1.61.0\n")
	for _, args := range [][]string{
		{"add", "endpoint", "notes/publish", "-method", "PUT", "-path", "/notes/{slug}/publish", "-guard", "signedIn", "-field", "at:date:optional", "-res", "published:bool"},
		{"add", "endpoint", "notes/count", "-method", "GET", "-res", "total:int"},
		{"add", "command", "seed"},
		{"add", "middleware", "loadUser"},
	} {
		if code := run(args, dir, &out, &errOut, fixedNow); code != 0 {
			t.Fatalf("%v: %s", args, errOut.String())
		}
	}
	if code := run([]string{"add", "endpoint", "notes/publish"}, dir, &out, &errOut, fixedNow); code == 0 {
		t.Fatal("an endpoint added twice")
	}
	goIn(t, dir, "mod", "tidy")
	goIn(t, dir, "vet", "./...")
	goIn(t, dir, "test", "-count=1", "./...")
	out.Reset()
	if code := run([]string{"routes"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("routes: %s", errOut.String())
	}
	for _, want := range []string{"PUT     /notes/{slug}/publish", "guards.SignedIn", "models.ReqPublishNotes", "GET     /notes/count", "commands: migrate, seed, users"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("routes lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := run([]string{"check"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("check: %d %s %s", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run([]string{"build", "-o", "bin/app"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("build: %s", errOut.String())
	}
	cmd := exec.Command(filepath.Join(dir, "bin/app"), "seed")
	cmd.Dir = dir
	if got, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(got), "seed: nothing yet") {
		t.Fatalf("seed: %v %s", err, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal("-ai wrote no AGENTS.md")
	}
}
