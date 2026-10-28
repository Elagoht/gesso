// Package mail sends email behind one small interface. Log (for development)
// and Capture (for tests) are here; a provider (SMTP, Resend, SES) is its own
// module.
package mail

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
)

// Message is one email.
type Message struct {
	From    string
	To      []string
	Cc      []string
	Bcc     []string
	ReplyTo string
	Subject string
	Text    string
	HTML    string
}

// ErrNoRecipient is returned for a message with no To, Cc or Bcc.
var ErrNoRecipient = errors.New("mail: no recipient")

// Check reports what is wrong with a message any mailer would refuse.
func (m Message) Check() error {
	if len(m.To)+len(m.Cc)+len(m.Bcc) == 0 {
		return ErrNoRecipient
	}
	if m.Text == "" && m.HTML == "" {
		return errors.New("mail: no body")
	}
	return nil
}

// Mailer sends messages.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Log writes each message's envelope to a logger instead of sending it — never
// its body, which may hold a password-reset link.
type Log struct {
	Logger *slog.Logger
}

// Send logs the message.
func (l Log) Send(ctx context.Context, m Message) error {
	if err := m.Check(); err != nil {
		return err
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "mail: not sent (log mailer)", "to", m.To, "subject", m.Subject)
	return nil
}

// Capture keeps every message it is given, for a test to read. It is safe for
// concurrent use.
type Capture struct {
	mu   sync.Mutex
	sent []Message
	// Err, when set, is returned by Send instead of keeping the message.
	Err error
}

// Send keeps the message.
func (c *Capture) Send(_ context.Context, m Message) error {
	if err := m.Check(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	c.sent = append(c.sent, m)
	return nil
}

// Sent is every message kept, in order.
func (c *Capture) Sent() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sent)
}

// Last is the newest message kept, or false.
func (c *Capture) Last() (Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return Message{}, false
	}
	return c.sent[len(c.sent)-1], true
}
