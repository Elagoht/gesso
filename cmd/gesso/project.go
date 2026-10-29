package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

// inspection mirrors gesso.Inspection: what a project's program prints for
// gesso-inspect. The CLI does not import gesso, so it keeps its own copy.
type inspection struct {
	Version  int         `json:"version"`
	Routes   []routeInfo `json:"routes"`
	Commands []string    `json:"commands"`
}

type routeInfo struct {
	Method  string    `json:"method"`
	Pattern string    `json:"pattern"`
	Module  string    `json:"module,omitempty"`
	Guards  []string  `json:"guards"`
	Input   *typeInfo `json:"input,omitempty"`
	Output  *typeInfo `json:"output,omitempty"`
	Status  int       `json:"status,omitempty"`
}

type typeInfo struct {
	Name   string `json:"name"`
	Fields []struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		From     string `json:"from"`
		Key      string `json:"key"`
		Validate string `json:"validate,omitempty"`
	} `json:"fields,omitempty"`
}

// inspectProject builds the project in dir, as gesso dev does, and runs it as
// "<program> gesso-inspect": it builds the app — no serving, no migrations —
// and prints it. pkg is the main package.
func inspectProject(dir, pkg string) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "gesso-inspect-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "app")
	var buildOut bytes.Buffer
	build := exec.Command("go", "build", "-o", bin, pkg)
	build.Dir, build.Stdout, build.Stderr = dir, &buildOut, &buildOut
	if err := build.Run(); err != nil {
		return nil, fmt.Errorf("the project does not build:\n%s", buildOut.String())
	}
	var out, errOut bytes.Buffer
	cmd := exec.Command(bin, "gesso-inspect")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &out, &errOut
	cmd.Env = append(os.Environ(), "GESSO_DEV=1")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s gesso-inspect: %v\n%s(it needs gesso v0.11.0 or later, and a main that calls gesso.Main)", pkg, err, errOut.String())
	}
	return out.Bytes(), nil
}

func loadInspection(dir, pkg string) (inspection, error) {
	raw, err := inspectProject(dir, pkg)
	if err != nil {
		return inspection{}, err
	}
	var in inspection
	if err := json.Unmarshal(raw, &in); err != nil {
		return inspection{}, fmt.Errorf("gesso-inspect printed no JSON: %w", err)
	}
	return in, nil
}

func inspectCommandRun(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("inspect")
	pkg := fs.String("pkg", ".", "the main package")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	raw, err := inspectProject(dir, *pkg)
	if err != nil {
		return err
	}
	_, err = stdout.Write(raw)
	return err
}

func routesCommand(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("routes")
	pkg := fs.String("pkg", ".", "the main package")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	in, err := loadInspection(dir, *pkg)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "METHOD\tPATH\tMODULE\tGUARDS\tIN\tOUT")
	for _, r := range in.Routes {
		guards := strings.Join(r.Guards, ", ")
		if guards == "" {
			guards = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Method, r.Pattern, orDash(r.Module), guards, typeName(r.Input), typeName(r.Output))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(in.Commands) > 0 {
		fmt.Fprintf(stdout, "\ncommands: %s\n", strings.Join(in.Commands, ", "))
	}
	return nil
}

func typeName(t *typeInfo) string {
	if t == nil {
		return "-"
	}
	return t.Name
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
