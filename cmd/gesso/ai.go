package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed scaffold/ai/AGENTS.md
var agentsTemplate string

const (
	agentsBegin = "<!-- gesso:begin"
	agentsEnd   = "<!-- gesso:end -->"
)

const projectRulesStub = "\n## Project rules\n\n<!-- Your own rules for agents. gesso never changes anything outside the markers above. -->\n"

// aiInit is gesso ai init: gesso's section of AGENTS.md, written or refreshed
// between its markers, and a CLAUDE.md importing AGENTS.md when there is none.
func aiInit(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("ai init")
	into := fs.String("dir", ".", "the project")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("ai init takes no arguments")
	}
	root := filepath.Join(dir, *into)
	version := gessoVersion()
	if version == "" {
		version = "devel"
	}
	section := strings.ReplaceAll(agentsTemplate, "@@VERSION@@", version)
	p := filepath.Join(root, "AGENTS.md")
	existing, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	merged, err := mergeAgents(existing, err == nil, section)
	if err != nil {
		return fmt.Errorf("%s: %w; nothing written", p, err)
	}
	if err := os.WriteFile(p, []byte(merged), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wrote", p)
	claude := filepath.Join(root, "CLAUDE.md")
	if _, err := os.Stat(claude); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(claude, []byte("@AGENTS.md\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "wrote", claude)
	} else if b, _ := os.ReadFile(claude); !strings.Contains(string(b), "@AGENTS.md") {
		fmt.Fprintf(stdout, "note: %s does not import AGENTS.md: add a line @AGENTS.md\n", claude)
	}
	return nil
}

// mergeAgents is existing with gesso's section replaced: a new file gets the
// section and a place for the project's own rules, one without markers keeps
// its text below the section. Markers that are not one begin and one end are
// refused: which text is gesso's would be a guess.
func mergeAgents(existing []byte, exists bool, section string) (string, error) {
	if !exists {
		return section + projectRulesStub, nil
	}
	text := string(existing)
	begin, end, offset := -1, -1, 0
	for line := range strings.SplitAfterSeq(text, "\n") {
		bare := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(bare, agentsBegin):
			if begin >= 0 {
				return "", errors.New("a second gesso:begin marker")
			}
			begin = offset
		case strings.HasPrefix(bare, agentsEnd):
			if begin < 0 || end >= 0 {
				return "", errors.New("a gesso:end marker out of place")
			}
			end = offset + len(line)
		}
		offset += len(line)
	}
	switch {
	case begin < 0:
		return section + "\n" + text, nil
	case end < 0:
		return "", errors.New("a gesso:begin marker without its end")
	}
	return text[:begin] + section + text[end:], nil
}
