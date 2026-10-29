package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

// cli is what a command runs with.
type cli struct {
	dir            string
	stdout, stderr io.Writer
	now            func() time.Time
}

// command is one of gesso's commands. run returns findings — stale files,
// broken rules — that make the exit code 1 without being an error.
type command struct {
	name    string // "add module"
	args    string // "<name>"
	summary string
	help    string
	run     func(c cli, args []string) (findings int, err error)
}

// usageError is a command used wrong — an unknown flag, a missing argument —
// as opposed to one that ran and failed: exit code 2, not 1.
type usageError struct{ error }

func usage(msg string) error { return usageError{errors.New(msg)} }

// newFlags is a command's flag set. Commands make theirs with it, so that help,
// completion and the man page can read the flags a command declares: run with
// -h, a command declares them and stops.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	declared = fs
	return fs
}

var declared *flag.FlagSet

// flagsOf runs cmd with -h to have it declare its flags.
func flagsOf(cmd command) *flag.FlagSet {
	declared = nil
	_, _ = cmd.run(cli{dir: ".", stdout: io.Discard, stderr: io.Discard, now: time.Now}, []string{"-h"})
	return declared
}

// parseFlags parses fs from args, which may put flags before and after the
// positional arguments, and returns those.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageError{withSuggestion(err, fs)}
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// withSuggestion adds the closest flag fs has to an unknown flag's error.
func withSuggestion(err error, fs *flag.FlagSet) error {
	name, ok := strings.CutPrefix(err.Error(), "flag provided but not defined: -")
	if !ok {
		return err
	}
	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	if s := closest(strings.TrimLeft(name, "-"), names); s != "" {
		return fmt.Errorf("%w (did you mean -%s?)", err, s)
	}
	return err
}

// closest is the name in names nearest word, or "" when none is near: at most
// a third of its letters off, or word a prefix of it.
func closest(word string, names []string) string {
	best, bestD := "", len(word)/3+1
	for _, n := range names {
		if strings.HasPrefix(n, word) && len(word) >= 2 {
			return n
		}
		if d := distance(word, n); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

// distance is the edit distance from a to b, a swap of two neighbours counting
// as one edit: "dmeo" is one from "demo".
func distance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

// find is the command args name, and the arguments after its name. A word
// that names no command is a usage error, with the closest name.
func find(args []string) (command, []string, error) {
	for _, n := range []int{2, 1} {
		if len(args) < n {
			continue
		}
		name := strings.Join(args[:n], " ")
		for _, c := range commands {
			if c.name == name {
				return c, args[n:], nil
			}
		}
	}
	var groups, top []string
	for _, c := range commands {
		if first, sub, ok := strings.Cut(c.name, " "); ok {
			if first == args[0] {
				groups = append(groups, sub)
			}
			top = append(top, first)
		} else {
			top = append(top, c.name)
		}
	}
	if len(groups) > 0 {
		if len(args) == 1 {
			return command{}, nil, usage(args[0] + " what? " + strings.Join(groups, ", "))
		}
		msg := fmt.Sprintf("unknown command %q", args[0]+" "+args[1])
		if s := closest(args[1], groups); s != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", args[0]+" "+s)
		}
		return command{}, nil, usageError{errors.New(msg)}
	}
	msg := fmt.Sprintf("unknown command %q", args[0])
	if s := closest(args[0], slices.Compact(sortedCopy(top))); s != "" {
		msg += fmt.Sprintf(" (did you mean %q?)", s)
	}
	return command{}, nil, usageError{errors.New(msg)}
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

// overview is gesso with no command, and gesso help.
func overview() string {
	var b strings.Builder
	b.WriteString("gesso builds JSON APIs in Go.\n\nusage: gesso <command> [arguments]\n\n")
	width := 0
	for _, c := range commands {
		width = max(width, len(c.name))
	}
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, c.name, c.summary)
	}
	b.WriteString("\ngesso help <command> says more about one.\n")
	return b.String()
}

// helpFor is gesso help <command>, and <command> -h.
func helpFor(c command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: gesso %s", c.name)
	if c.args != "" {
		b.WriteString(" " + c.args)
	}
	fs := flagsOf(c)
	hasFlags := false
	if fs != nil {
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
	}
	if hasFlags {
		b.WriteString(" [flags]")
	}
	b.WriteString("\n\n" + c.summary + ".\n")
	if c.help != "" {
		b.WriteString("\n" + strings.TrimSpace(c.help) + "\n")
	}
	if hasFlags {
		b.WriteString("\nflags:\n")
		fs.SetOutput(&b)
		fs.PrintDefaults()
	}
	return b.String()
}
