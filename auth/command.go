package auth

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Command manages the users from the command line, for App.Command:
//
//	app.Command("users", users.Command(os.Stdin))
//
//	app users add <username> [-role admin]...   asks for the password
//	app users password <username>               sets a new one; signs them out
//	app users roles <username> [role]...        replaces the roles; signs them out
//	app users delete <username>
//	app users list
//
// A password is read from in: without echo when in is a terminal, asked twice;
// otherwise its first line, for a script. It is never an argument, which the
// shell's history and the process list would keep. The users' table is made
// first, so the first user can be added before the app has ever served.
func (s *SQL) Command(in io.Reader) func(ctx context.Context, args []string, out io.Writer) error {
	return func(ctx context.Context, args []string, out io.Writer) error {
		if len(args) == 0 {
			return errors.New(commandUsage)
		}
		if err := migrateUsers(ctx, s.db, s.dialect); err != nil {
			return err
		}
		read := passwordReader(in, out)
		switch verb, rest := args[0], args[1:]; verb {
		case "add":
			fs := flag.NewFlagSet("users add", flag.ContinueOnError)
			fs.SetOutput(out)
			var roles multi
			fs.Var(&roles, "role", "a role the user holds; repeat for more")
			name, err := oneName(fs, rest)
			if err != nil {
				return err
			}
			pass, err := read()
			if err != nil {
				return err
			}
			u, err := s.Create(ctx, name, pass, roles...)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "added %s (id %s)\n", u.Username, u.ID)
			return nil
		case "password":
			name, err := oneName(flag.NewFlagSet("users password", flag.ContinueOnError), rest)
			if err != nil {
				return err
			}
			pass, err := read()
			if err != nil {
				return err
			}
			if err := s.SetPassword(ctx, name, pass); err != nil {
				return err
			}
			fmt.Fprintf(out, "set the password of %s, and signed them out\n", name)
			return nil
		case "roles":
			if len(rest) == 0 {
				return errors.New("usage: users roles <username> [roles]")
			}
			if err := s.SetRoles(ctx, rest[0], rest[1:]...); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s holds %s, and is signed out\n", rest[0], rolesText(rest[1:]))
			return nil
		case "delete":
			name, err := oneName(flag.NewFlagSet("users delete", flag.ContinueOnError), rest)
			if err != nil {
				return err
			}
			if err := s.Delete(ctx, name); err != nil {
				return err
			}
			fmt.Fprintf(out, "deleted %s\n", name)
			return nil
		case "list":
			users, err := s.List(ctx)
			if err != nil {
				return err
			}
			for _, u := range users {
				fmt.Fprintf(out, "%s\t%s\t%s\n", u.ID, u.Username, rolesText(u.Roles))
			}
			return nil
		}
		return fmt.Errorf("users: no command %q\n%s", args[0], commandUsage)
	}
}

const commandUsage = `usage:
  users add <username> [-role admin]...
  users password <username>
  users roles <username> [role]...
  users delete <username>
  users list`

func oneName(fs *flag.FlagSet, args []string) (string, error) {
	// The username first, then the flags: flag stops at the first argument.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("usage: %s <username>", fs.Name())
	}
	if err := fs.Parse(args[1:]); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("%s takes one username", fs.Name())
	}
	return args[0], nil
}

// passwordReader reads a password from in: from a terminal twice, without
// echo; from anything else, its first line.
func passwordReader(in io.Reader, out io.Writer) func() (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return func() (string, error) {
			ask := func(prompt string) (string, error) {
				fmt.Fprint(out, prompt)
				b, err := term.ReadPassword(int(f.Fd()))
				fmt.Fprintln(out)
				return string(b), err
			}
			first, err := ask("password: ")
			if err != nil {
				return "", err
			}
			again, err := ask("again: ")
			if err != nil {
				return "", err
			}
			if first != again {
				return "", errors.New("the passwords differ")
			}
			return first, nil
		}
	}
	lines := bufio.NewReader(in)
	return func() (string, error) {
		line, err := lines.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return "", errors.New("no password on standard input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func rolesText(roles []string) string {
	if len(roles) == 0 {
		return "no roles"
	}
	return strings.Join(roles, ", ")
}
