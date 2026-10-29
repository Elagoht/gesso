package gesso

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// terminalHandler writes a record as a person reads it on a terminal: the
// time, a marker for the level, the message, then the attributes, dimmed.
//
//	13:50:29 • gesso: listening  addr=127.0.0.1:8080
//	13:50:30 ✗ request  method=GET path=/docs status=500
type terminalHandler struct {
	mu     *sync.Mutex
	w      io.Writer
	color  bool
	attrs  string // the attributes of With, written already
	prefix string // the groups of WithGroup, "a.b."
}

func newTerminalHandler(w io.Writer, color bool) *terminalHandler {
	return &terminalHandler{mu: &sync.Mutex{}, w: w, color: color}
}

func (h *terminalHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (h *terminalHandler) Handle(_ context.Context, r slog.Record) error {
	var b bytes.Buffer
	marker, tint := "•", "36" // cyan
	switch {
	case r.Level >= slog.LevelError:
		marker, tint = "✗", "31"
	case r.Level >= slog.LevelWarn:
		marker, tint = "▲", "33"
	}
	if !h.color {
		marker = map[string]string{"•": "-", "▲": "!", "✗": "x"}[marker]
	}
	b.WriteString(r.Time.Format(time.TimeOnly))
	b.WriteByte(' ')
	b.WriteString(h.paint(tint, marker))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	var attrs strings.Builder
	attrs.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&attrs, h.prefix, a)
		return true
	})
	if attrs.Len() > 0 {
		b.WriteString(" ")
		b.WriteString(h.paint("2", attrs.String()))
	}
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(b.Bytes())
	return err
}

func (h *terminalHandler) paint(code, s string) string {
	if !h.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			writeAttr(b, p, ga)
		}
		return
	}
	b.WriteString(" ")
	b.WriteString(prefix + a.Key + "=")
	v := a.Value.String()
	if strings.ContainsAny(v, " \t\"=") || v == "" {
		v = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	b.WriteString(v)
}

func (h *terminalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		writeAttr(&b, h.prefix, a)
	}
	c := *h
	c.attrs = b.String()
	return &c
}

func (h *terminalHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix += name + "."
	return &c
}

// onTerminal reports whether w is a terminal a person reads — or FORCE_COLOR
// says to write as if it were — and whether in colour: not under NO_COLOR, nor
// on a dumb terminal.
func onTerminal(w io.Writer) (terminal, color bool) {
	f, ok := w.(*os.File)
	terminal = os.Getenv("FORCE_COLOR") != "" || (ok && term.IsTerminal(int(f.Fd())))
	color = terminal && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	return terminal, color
}

// TerminalLogger writes records for a person reading w: one line each, the
// time, a marker for the level, the message, the attributes after it. In
// colour unless NO_COLOR is set, TERM is dumb, or w is not a terminal (and
// FORCE_COLOR does not say otherwise). gesso.Main gives it to an app whose
// standard error is a terminal.
func TerminalLogger(w io.Writer) *slog.Logger {
	_, color := onTerminal(w)
	return slog.New(newTerminalHandler(w, color))
}
