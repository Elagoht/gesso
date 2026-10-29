package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Command runs a migrate command on m, writing what it did to out — the
// "migrate" command of an app's binary:
//
//	status      every migration, applied or pending
//	up          apply the pending ones
//	down [n]    revert the newest n (1)
func Command(ctx context.Context, m *Migrator, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("want status, up or down [n]")
	}
	switch args[0] {
	case "status":
		st, err := m.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range st {
			fmt.Fprintf(out, "%d  %-8s %s\n", s.Version, s.State, s.Name)
		}
		return nil
	case "up":
		done, err := m.Up(ctx)
		for _, mg := range done {
			fmt.Fprintf(out, "applied  %d %s\n", mg.Version, mg.Name)
		}
		return err
	case "down":
		n := 1
		if len(args) > 1 {
			var err error
			if n, err = strconv.Atoi(args[1]); err != nil || n < 1 {
				return fmt.Errorf("down takes a count of 1 or more, got %q", args[1])
			}
		}
		done, err := m.Down(ctx, n)
		for _, mg := range done {
			fmt.Fprintf(out, "reverted %d %s\n", mg.Version, mg.Name)
		}
		return err
	}
	return fmt.Errorf("unknown %q: want status, up or down [n]", args[0])
}
