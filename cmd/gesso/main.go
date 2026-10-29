// Command gesso is gesso's command line; gesso help lists its commands.
//
// Applying migrations needs the application's database driver, which this binary
// does not have: up and down run in the application's own binary, through the
// migrate package.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/tools/imports"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr, time.Now))
}

// commands are gesso's commands, in the order help lists them. They are set in
// init: help, completion and man read the list they are in.
var commands []command

func init() {
	commands = []command{
		{name: "new", args: "<module path>", summary: "make a project: main.go, register.go, a test",
			help: "A module path such as example.com/<name>; the folder is its last element unless -dir names one.",
			run:  func(c cli, a []string) (int, error) { return 0, newProject(a, c.dir, c.stdout, c.now) }},
		{name: "init", summary: "add internal/api — api.go, register.go — to the Go module here, a collage site say",
			run: func(c cli, a []string) (int, error) { return 0, initProject(a, c.dir, c.stdout) }},
		{name: "dev", args: "[-- program args]", summary: "run the project, rebuilt and restarted as files change",
			help: "Reads .env.development, or .env. A failed build leaves the running program up.",
			run: func(c cli, a []string) (int, error) {
				ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				return 0, devCommand(ctx, a, c.dir, c.stdout, c.stderr)
			}},
		{name: "build", summary: "compile the binary you deploy",
			help: buildHelp,
			run:  func(c cli, a []string) (int, error) { return 0, buildCommand(a, c.dir, c.stdout, os.Stdin) }},
		{name: "routes", summary: "list the endpoints the project serves",
			run: func(c cli, a []string) (int, error) { return 0, routesCommand(a, c.dir, c.stdout) }},
		{name: "inspect", summary: "print the project's routes, types and commands as JSON",
			run: func(c cli, a []string) (int, error) { return 0, inspectCommandRun(a, c.dir, c.stdout) }},
		{name: "check", args: "[packages]", summary: "check the project: ctx rules, stale generated files, unguarded writes",
			help: checkHelp,
			run:  func(c cli, a []string) (int, error) { return check(a, c.dir, c.stdout) }},
		{name: "gen", args: "[dirs]", summary: "write the code models and queries.sql describe, and the migration for a model's change",
			run: func(c cli, a []string) (int, error) { return gen(a, c.dir, c.stdout, c.now) }},
		{name: "migrate new", args: "<name>", summary: "write an empty migration pair",
			run: func(c cli, a []string) (int, error) { return 0, migrateNew(a, c.dir, c.stdout, c.now) }},
		{name: "add module", args: "<name>", summary: "a CRUD module from its fields, wired into register.go",
			help: addModuleHelp,
			run:  func(c cli, a []string) (int, error) { return 0, addModule(a, c.dir, c.stdout, c.now) }},
		{name: "add endpoint", args: "<module>/<name>", summary: "one endpoint in a module: its route, handler, Req and Res",
			run: func(c cli, a []string) (int, error) { return 0, addEndpoint(a, c.dir, c.stdout) }},
		{name: "add auth", summary: "sign-in: a users table, a users command, a signedIn guard",
			run: func(c cli, a []string) (int, error) { return 0, addAuth(a, c.dir, c.stdout) }},
		{name: "add guard", args: "<name>", summary: "internal/guards/<name>.go, for -guard",
			run: func(c cli, a []string) (int, error) { return 0, addGuard(a, c.dir, c.stdout) }},
		{name: "add command", args: "<name>", summary: "internal/commands/<name>.go: app <name>, wired into register.go",
			run: func(c cli, a []string) (int, error) { return 0, addCommand(a, c.dir, c.stdout) }},
		{name: "add middleware", args: "<name>", summary: "internal/middleware/<name>.go around every route, wired into register.go",
			run: func(c cli, a []string) (int, error) { return 0, addMiddleware(a, c.dir, c.stdout) }},
		{name: "ai init", summary: "write gesso's rules for coding agents into AGENTS.md",
			run: func(c cli, a []string) (int, error) { return 0, aiInit(a, c.dir, c.stdout) }},
		{name: "completion", args: "bash|zsh|fish", summary: "a shell completion script",
			run: func(c cli, a []string) (int, error) { return 0, completion(a, c.stdout) }},
		{name: "man", summary: "the manual page; -install puts it where man finds it",
			run: func(c cli, a []string) (int, error) { return 0, manCommand(a, c.stdout) }},
		{name: "version", summary: "which gesso this is",
			run: func(c cli, a []string) (int, error) {
				if _, err := parseFlags(newFlags("version"), a); err != nil {
					return 0, err
				}
				fmt.Fprintln(c.stdout, versionLine())
				return 0, nil
			}},
	}
	commands = append(commands, command{name: "help", args: "[command]", summary: "this list, or what one command does",
		run: func(c cli, a []string) (int, error) {
			pos, err := parseFlags(newFlags("help"), a)
			if err != nil || len(pos) == 0 {
				fmt.Fprint(c.stdout, overview())
				return 0, err
			}
			cmd, rest, err := find(pos)
			if err != nil {
				return 0, err
			}
			if len(rest) > 0 {
				return 0, usage("help takes one command")
			}
			fmt.Fprint(c.stdout, helpFor(cmd))
			return 0, nil
		}})
}

// run is main without the exit, for the tests. dir is where paths are relative
// to. It exits 0 when all is well, 1 when a command failed or found something,
// 2 when it was used wrong.
func run(args []string, dir string, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, overview())
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		args = []string{"help"}
	case "-v", "--version":
		args = []string{"version"}
	}
	cmd, rest, err := find(args)
	if err != nil {
		fmt.Fprintf(stderr, "gesso: %v\n", err)
		return 2
	}
	// Code gesso writes groups the project's imports apart, as goimports does.
	imports.LocalPrefix, _ = readModule(dir)
	findings, err := cmd.run(cli{dir: dir, stdout: stdout, stderr: stderr, now: now}, rest)
	var ue usageError
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, helpFor(cmd))
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "gesso %s: %v\nusage: gesso %s %s (gesso help %s)\n", cmd.name, ue.error, cmd.name, cmd.args, cmd.name)
		return 2
	case err != nil:
		fmt.Fprintf(stderr, "gesso: %v\n", err)
		return 1
	case findings > 0:
		return 1
	}
	return 0
}
