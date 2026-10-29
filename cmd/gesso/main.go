// Command gesso is gesso's command line:
//
//	gesso new <module path> [-dialect sqlite|postgres|mysql] [-dir name]
//	gesso init [-dialect sqlite|postgres|mysql]
//	gesso dev [-pkg .] [-- program args]
//	gesso migrate new <name> [-dir migrations]
//	gesso add module <name> [-fields title:string,seats:int] [-empty] [-dialect postgres] [-dir internal]
//	gesso add auth [-users single|sql] [-dir internal]
//	gesso check [packages]
//	gesso gen [dirs] [-check]
//
// Applying migrations needs the application's database driver, which this binary
// does not have: up and down run in the application's own binary, through the
// migrate package.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr, time.Now))
}

const usage = `usage:
  gesso new <module path> [-dialect sqlite|postgres|mysql] [-dir name]
  gesso init [-dialect sqlite|postgres|mysql]   internal/api in an existing module
  gesso dev [-pkg .] [-- args]   run it, with .env; rebuild and restart on changes
  gesso migrate new <name> [-dir migrations]
  gesso add module <name> [-fields title:string,seats:int] [-empty] [-dialect postgres|sqlite|mysql] [-dir internal]
  gesso add auth [-users single|sql] [-dir internal]   sign-in, from gesso/auth
  gesso check [packages]   (default ./...)
  gesso gen [dirs] [-check]   queries.sql → queries.gen.go; -check reports stale files
`

// run is main without the exit, for the tests. dir is where paths are relative to.
func run(args []string, dir string, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	code := 0
	switch {
	case args[0] == "new":
		err = newProject(args[1:], dir, stdout)
	case args[0] == "init":
		err = initProject(args[1:], dir, stdout)
	case args[0] == "dev":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = devCommand(ctx, args[1:], dir, stdout, stderr)
		stop()
	case len(args) >= 2 && args[0] == "migrate" && args[1] == "new":
		err = migrateNew(args[2:], dir, stdout, now)
	case len(args) >= 2 && args[0] == "add" && args[1] == "module":
		err = addModule(args[2:], dir, stdout, now)
	case len(args) >= 2 && args[0] == "add" && args[1] == "auth":
		err = addAuth(args[2:], dir, stdout)
	case args[0] == "check":
		var findings int
		findings, err = check(args[1:], dir, stdout)
		if err == nil && findings > 0 {
			code = 1
		}
	case args[0] == "gen":
		var stale int
		stale, err = gen(args[1:], dir, stdout)
		if err == nil && stale > 0 {
			code = 1
		}
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "gesso: unknown command %q\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "gesso: %v\n", err)
		return 2
	}
	return code
}
