package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// commandFlags is each command's flags, as it declares them.
func commandFlags(c command) []*flag.Flag {
	var out []*flag.Flag
	if fs := flagsOf(c); fs != nil {
		fs.VisitAll(func(f *flag.Flag) { out = append(out, f) })
	}
	return out
}

// words are the first words of the commands, and the second words of each.
func words() (top []string, subs map[string][]string) {
	subs = map[string][]string{}
	for _, c := range commands {
		first, sub, two := strings.Cut(c.name, " ")
		if !slices.Contains(top, first) {
			top = append(top, first)
		}
		if two {
			subs[first] = append(subs[first], sub)
		}
	}
	return top, subs
}

func completion(args []string, out io.Writer) error {
	pos, err := parseFlags(newFlags("completion"), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("completion takes bash, zsh or fish")
	}
	top, subs := words()
	switch pos[0] {
	case "bash", "zsh":
		var b strings.Builder
		if pos[0] == "zsh" {
			b.WriteString("# gesso completion zsh > \"${fpath[1]}/_gesso\", or: source <(gesso completion zsh)\nautoload -U +X bashcompinit && bashcompinit\n")
		} else {
			b.WriteString("# source <(gesso completion bash), e.g. in ~/.bashrc\n")
		}
		b.WriteString("_gesso() {\n\tlocal cur=\"${COMP_WORDS[COMP_CWORD]}\" cmd=\"${COMP_WORDS[1]}\"\n")
		b.WriteString("\tif [ \"$COMP_CWORD\" -eq 1 ]; then COMPREPLY=($(compgen -W \"" + strings.Join(top, " ") + "\" -- \"$cur\")); return; fi\n")
		b.WriteString("\tcase \"$cmd\" in\n")
		for first, s := range subs {
			fmt.Fprintf(&b, "\t%s) if [ \"$COMP_CWORD\" -eq 2 ]; then COMPREPLY=($(compgen -W %q -- \"$cur\")); return; fi; cmd=\"$cmd ${COMP_WORDS[2]}\";;\n", first, strings.Join(s, " "))
		}
		b.WriteString("\tesac\n\tlocal flags=\"\"\n\tcase \"$cmd\" in\n")
		for _, c := range commands {
			var names []string
			for _, f := range commandFlags(c) {
				names = append(names, "-"+f.Name)
			}
			if len(names) > 0 {
				fmt.Fprintf(&b, "\t%q) flags=%q;;\n", c.name, strings.Join(names, " "))
			}
		}
		b.WriteString("\tesac\n\tif [[ \"$cur\" == -* ]]; then COMPREPLY=($(compgen -W \"$flags\" -- \"$cur\")); else COMPREPLY=($(compgen -f -- \"$cur\")); fi\n}\ncomplete -F _gesso gesso\n")
		_, err := io.WriteString(out, b.String())
		return err
	case "fish":
		var b strings.Builder
		b.WriteString("# gesso completion fish > ~/.config/fish/completions/gesso.fish\ncomplete -c gesso -f\n")
		for _, c := range commands {
			first, sub, two := strings.Cut(c.name, " ")
			if !two {
				fmt.Fprintf(&b, "complete -c gesso -n __fish_use_subcommand -a %s -d %q\n", first, c.summary)
			} else {
				fmt.Fprintf(&b, "complete -c gesso -n '__fish_seen_subcommand_from %s; and not __fish_seen_subcommand_from %s' -a %s -d %q\n", first, strings.Join(subs[first], " "), sub, c.summary)
			}
			cond := "__fish_seen_subcommand_from " + first
			if two {
				cond = "__fish_seen_subcommand_from " + first + "; and __fish_seen_subcommand_from " + sub
			}
			for _, f := range commandFlags(c) {
				fmt.Fprintf(&b, "complete -c gesso -n '%s' -o %s -d %q\n", cond, f.Name, firstLine(f.Usage))
			}
		}
		_, err := io.WriteString(out, b.String())
		return err
	}
	msg := fmt.Sprintf("unknown shell %q: bash, zsh or fish", pos[0])
	if c := closest(pos[0], []string{"bash", "zsh", "fish"}); c != "" {
		msg += fmt.Sprintf(" (did you mean %q?)", c)
	}
	return usage(msg)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func manCommand(args []string, out io.Writer) error {
	fs := newFlags("man")
	install := fs.Bool("install", false, "write it to ~/.local/share/man/man1/gesso.1, where man finds it")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	page := manPage()
	if !*install {
		_, err := io.WriteString(out, page)
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	p := filepath.Join(home, ".local", "share", "man", "man1", "gesso.1")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(page), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s: man gesso\n", p)
	return nil
}

// roff escapes text for a man page.
func roff(s string) string {
	s = strings.ReplaceAll(s, `\`, `\e`)
	var b strings.Builder
	for line := range strings.Lines(s) {
		if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "'") {
			b.WriteString(`\&`)
		}
		b.WriteString(line)
	}
	return b.String()
}

func manPage() string {
	var b strings.Builder
	b.WriteString(".TH GESSO 1 \"\" \"gesso\" \"gesso manual\"\n.SH NAME\ngesso \\- build JSON APIs in Go\n.SH SYNOPSIS\n.B gesso\n.I command\n[arguments]\n.SH COMMANDS\n")
	for _, c := range commands {
		fmt.Fprintf(&b, ".TP\n.B gesso %s %s\n%s.\n", c.name, roff(c.args), roff(c.summary))
		if c.help != "" {
			b.WriteString(".IP\n.nf\n" + roff(strings.TrimSpace(c.help)) + "\n.fi\n")
		}
		for _, f := range commandFlags(c) {
			fmt.Fprintf(&b, ".RS\n.TP\n.B \\-%s\n%s\n.RE\n", f.Name, roff(f.Usage))
		}
	}
	b.WriteString(".SH EXIT STATUS\n0 when all is well; 1 when a command failed or found something; 2 when it was used wrong.\n.SH SEE ALSO\nhttps://github.com/Elagoht/gesso\n")
	return b.String()
}
