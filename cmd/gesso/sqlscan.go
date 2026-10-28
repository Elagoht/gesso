package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// The SQL gesso gen reads is not parsed, only scanned: enough to count the
// placeholders and to name the result columns, skipping what may hide a "?", a
// "$1" or a comma — strings, quoted identifiers, comments, dollar-quoted bodies,
// casts — and keeping track of parentheses.

type tokKind int

const (
	tWord   tokKind = iota // a keyword or identifier (quoted ones unquoted)
	tPlace                 // a placeholder: $n, or ? outside PostgreSQL
	tPunct                 // ( ) , . * ; and the rest
	tString                // a literal string or dollar-quoted body
	tNumber
)

type tok struct {
	kind  tokKind
	text  string
	depth int // parentheses around it
	n     int // a $n placeholder's number
}

func scanSQL(sql, dialect string) ([]tok, error) {
	var out []tok
	depth := 0
	r := []rune(sql)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '-' && i+1 < len(r) && r[i+1] == '-':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			end := strings.Index(string(r[i+2:]), "*/")
			if end < 0 {
				return nil, fmt.Errorf("an unclosed /* comment")
			}
			i += 2 + len([]rune(string(r[i+2:])[:end])) + 2
		case c == '\'':
			j := i + 1
			for ; j < len(r); j++ {
				if r[j] == '\'' {
					if j+1 < len(r) && r[j+1] == '\'' {
						j++ // '' is a quote inside the string
						continue
					}
					break
				}
			}
			if j >= len(r) {
				return nil, fmt.Errorf("an unclosed string")
			}
			out = append(out, tok{kind: tString, text: string(r[i : j+1]), depth: depth})
			i = j + 1
		case c == '"' || c == '`':
			j := i + 1
			for j < len(r) && r[j] != c {
				j++
			}
			if j >= len(r) {
				return nil, fmt.Errorf("an unclosed quoted identifier")
			}
			out = append(out, tok{kind: tWord, text: string(r[i+1 : j]), depth: depth})
			i = j + 1
		case c == '$' && dialect == "postgres":
			j := i + 1
			if j < len(r) && unicode.IsDigit(r[j]) {
				for j < len(r) && unicode.IsDigit(r[j]) {
					j++
				}
				n, _ := strconv.Atoi(string(r[i+1 : j]))
				out = append(out, tok{kind: tPlace, text: string(r[i:j]), depth: depth, n: n})
				i = j
				continue
			}
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_') {
				j++
			}
			if j >= len(r) || r[j] != '$' {
				return nil, fmt.Errorf("a stray $")
			}
			tag := string(r[i : j+1])
			end := strings.Index(string(r[j+1:]), tag)
			if end < 0 {
				return nil, fmt.Errorf("an unclosed %s body", tag)
			}
			body := len([]rune(string(r[j+1:])[:end]))
			out = append(out, tok{kind: tString, text: string(r[i : j+1+body+len([]rune(tag))]), depth: depth})
			i = j + 1 + body + len([]rune(tag))
		case c == '?' && dialect != "postgres":
			out = append(out, tok{kind: tPlace, text: "?", depth: depth})
			i++
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_' || r[j] == '$') {
				j++
			}
			out = append(out, tok{kind: tWord, text: string(r[i:j]), depth: depth})
			i = j
		case unicode.IsDigit(c):
			j := i
			for j < len(r) && (unicode.IsDigit(r[j]) || r[j] == '.') {
				j++
			}
			out = append(out, tok{kind: tNumber, text: string(r[i:j]), depth: depth})
			i = j
		default:
			if c == ')' {
				depth--
				if depth < 0 {
					return nil, fmt.Errorf("a ) with no (")
				}
			}
			text := string(c)
			if c == ':' && i+1 < len(r) && r[i+1] == ':' {
				text = "::"
				i++
			}
			out = append(out, tok{kind: tPunct, text: text, depth: depth})
			if c == '(' {
				depth++
			}
			i++
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("a ( with no )")
	}
	return out, nil
}

// placeholders counts the parameters a statement takes: distinct $n numbers,
// which must run 1..N, or the ? marks.
func placeholders(toks []tok, dialect string) (int, error) {
	if dialect != "postgres" {
		n := 0
		for _, t := range toks {
			if t.kind == tPlace {
				n++
			}
		}
		return n, nil
	}
	seen := map[int]bool{}
	highest := 0
	for _, t := range toks {
		if t.kind == tPlace {
			seen[t.n] = true
			highest = max(highest, t.n)
		}
	}
	for i := 1; i <= highest; i++ {
		if !seen[i] {
			return 0, fmt.Errorf("$%d is never used, though $%d is", i, highest)
		}
	}
	return highest, nil
}

func isWord(t tok, w string) bool { return t.kind == tWord && strings.EqualFold(t.text, w) }

// resultColumns names the columns a statement returns: its top-level RETURNING
// list if it has one, else its first top-level SELECT list. Each item is a
// column (its last dotted part names it) or an expression with an AS alias.
func resultColumns(toks []tok) ([]string, error) {
	start, end := -1, len(toks)
	for i, t := range toks {
		if t.depth == 0 && isWord(t, "returning") {
			start = i + 1
		}
	}
	if start < 0 {
		for i, t := range toks {
			if t.depth == 0 && isWord(t, "select") {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, fmt.Errorf("it returns no columns: no SELECT or RETURNING")
		}
		for i := start; i < len(toks); i++ {
			if toks[i].depth == 0 && (isWord(toks[i], "from") || isWord(toks[i], "into") ||
				isWord(toks[i], "union") || isWord(toks[i], "where") || isWord(toks[i], "order") ||
				isWord(toks[i], "limit") || isWord(toks[i], "group") || isWord(toks[i], "having")) {
				end = i
				break
			}
		}
	}
	if start < len(toks) && (isWord(toks[start], "distinct") || isWord(toks[start], "all")) {
		start++
	}
	var cols []string
	var item []tok
	flush := func() error {
		if len(item) == 0 {
			return fmt.Errorf("an empty column")
		}
		name, err := columnName(item)
		if err != nil {
			return err
		}
		cols = append(cols, name)
		item = nil
		return nil
	}
	for i := start; i < end; i++ {
		t := toks[i]
		if t.depth == 0 && t.kind == tPunct && t.text == ";" {
			break
		}
		if t.depth == 0 && t.kind == tPunct && t.text == "," {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		item = append(item, t)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return cols, nil
}

func columnName(item []tok) (string, error) {
	text := joinToks(item)
	n := len(item)
	if n >= 2 && item[n-1].kind == tWord && isWord(item[n-2], "as") {
		return strings.ToLower(item[n-1].text), nil
	}
	if item[n-1].kind == tPunct && item[n-1].text == "*" {
		return "", fmt.Errorf("%q: name the columns, not *", text)
	}
	// a, t.a, schema.t.a
	plain := item[0].kind == tWord
	for i := 1; i < n && plain; i++ {
		if i%2 == 1 {
			plain = item[i].kind == tPunct && item[i].text == "."
		} else {
			plain = item[i].kind == tWord
		}
	}
	if plain && n%2 == 1 {
		return strings.ToLower(item[n-1].text), nil
	}
	return "", fmt.Errorf("%q: give the expression a name with AS", text)
}

func joinToks(ts []tok) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 && t.kind != tPunct && ts[i-1].kind != tPunct {
			b.WriteByte(' ')
		}
		b.WriteString(t.text)
	}
	return b.String()
}
