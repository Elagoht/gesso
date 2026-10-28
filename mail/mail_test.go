package mail

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestCapture(t *testing.T) {
	c := &Capture{}
	ctx := context.Background()
	if err := c.Send(ctx, Message{Subject: "x", Text: "b"}); !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("got %v", err)
	}
	_ = c.Send(ctx, Message{To: []string{"a@b.c"}, Subject: "one", Text: "1"})
	_ = c.Send(ctx, Message{To: []string{"a@b.c"}, Subject: "two", Text: "2"})
	if last, ok := c.Last(); !ok || last.Subject != "two" || len(c.Sent()) != 2 {
		t.Fatalf("got %+v", c.Sent())
	}
	c.Err = errors.New("provider down")
	if err := c.Send(ctx, Message{To: []string{"a@b.c"}, Text: "3"}); err == nil || len(c.Sent()) != 2 {
		t.Fatal("Err fails the send")
	}
}

func TestLogNeverWritesTheBody(t *testing.T) {
	var buf bytes.Buffer
	l := Log{Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	_ = l.Send(context.Background(), Message{To: []string{"a@b.c"}, Subject: "Reset", Text: "https://x/reset?token=SECRET"})
	if !strings.Contains(buf.String(), "Reset") || strings.Contains(buf.String(), "SECRET") {
		t.Fatalf("got %s", buf.String())
	}
}
