package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// Security is how an SMTP connection is protected.
type Security int

const (
	// StartTLS connects in plain text and upgrades with STARTTLS before
	// anything else is said; a server that cannot is refused. Port 587.
	StartTLS Security = iota
	// ImplicitTLS speaks TLS from the first byte. Port 465.
	ImplicitTLS
	// Plain sends everything in clear text: only for a server on this machine
	// (a development mail catcher), never across a network.
	Plain
)

// SMTP sends through an SMTP server.
type SMTP struct {
	// Addr is the server, "smtp.example.com:587".
	Addr string
	// Username and Password authenticate (AUTH PLAIN), when Username is set.
	Username, Password string
	// Security: StartTLS by default.
	Security Security
	// From is used for a message that names none.
	From string
	// Hello is the name this side gives in EHLO. Default "localhost".
	Hello string
	// TLS configures the connection's TLS; nil verifies the server's
	// certificate against the host in Addr.
	TLS *tls.Config
	// Timeout bounds a whole send when ctx has no deadline. Default 30s.
	Timeout time.Duration
}

// Send sends m: one SMTP transaction, every recipient — To, Cc and Bcc — in
// its envelope, Bcc in no header.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	if err := m.Check(); err != nil {
		return err
	}
	if m.From == "" {
		m.From = s.From
	}
	data, from, rcpts, err := build(m)
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		timeout := s.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return fmt.Errorf("mail: smtp addr %q: %w", s.Addr, err)
	}
	if s.Security == Plain && !isLocal(host) {
		return fmt.Errorf("mail: Plain SMTP to %s would send the password and the mail in clear text; use StartTLS", host)
	}
	tlsConfig := s.TLS
	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}
	var conn net.Conn
	d := &net.Dialer{}
	if s.Security == ImplicitTLS {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsConfig}).DialContext(ctx, "tcp", s.Addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", s.Addr)
	}
	if err != nil {
		return fmt.Errorf("mail: smtp: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: smtp: %w", err)
	}
	defer c.Close()
	if err := s.transact(c, tlsConfig, from, rcpts, data); err != nil {
		return fmt.Errorf("mail: smtp: %w", err)
	}
	return c.Quit()
}

func (s *SMTP) transact(c *smtp.Client, tlsConfig *tls.Config, from string, rcpts []string, data []byte) error {
	hello := s.Hello
	if hello == "" {
		hello = "localhost"
	}
	if err := c.Hello(hello); err != nil {
		return err
	}
	if s.Security == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.Username != "" {
		host, _, _ := net.SplitHostPort(s.Addr)
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Close()
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// build writes m as an RFC 5322 message, and returns its envelope: the sender
// and every recipient.
func build(m Message) (data []byte, from string, rcpts []string, err error) {
	fromAddr, err := address("From", m.From)
	if err != nil {
		return nil, "", nil, err
	}
	var to, cc []string
	for _, list := range []struct {
		name  string
		addrs []string
		into  *[]string
	}{{"To", m.To, &to}, {"Cc", m.Cc, &cc}, {"Bcc", m.Bcc, nil}} {
		for _, a := range list.addrs {
			parsed, err := address(list.name, a)
			if err != nil {
				return nil, "", nil, err
			}
			rcpts = append(rcpts, parsed.Address)
			if list.into != nil {
				*list.into = append(*list.into, parsed.String())
			}
		}
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, "", nil, errors.New("mail: the subject holds a line break")
	}
	var b bytes.Buffer
	h := textproto.MIMEHeader{}
	h.Set("From", fromAddr.String())
	if len(to) > 0 {
		h.Set("To", strings.Join(to, ", "))
	}
	if len(cc) > 0 {
		h.Set("Cc", strings.Join(cc, ", "))
	}
	if m.ReplyTo != "" {
		rt, err := address("Reply-To", m.ReplyTo)
		if err != nil {
			return nil, "", nil, err
		}
		h.Set("Reply-To", rt.String())
	}
	h.Set("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h.Set("Date", time.Now().UTC().Format(time.RFC1123Z))
	h.Set("Message-ID", messageID(fromAddr.Address))
	h.Set("MIME-Version", "1.0")
	var body bytes.Buffer
	switch {
	case m.Text != "" && m.HTML != "":
		mw := multipart.NewWriter(&body)
		h.Set("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
		for _, part := range []struct{ ctype, text string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
			ph := textproto.MIMEHeader{}
			ph.Set("Content-Type", part.ctype+"; charset=utf-8")
			ph.Set("Content-Transfer-Encoding", "quoted-printable")
			pw, err := mw.CreatePart(ph)
			if err != nil {
				return nil, "", nil, err
			}
			if err := writeQP(pw, part.text); err != nil {
				return nil, "", nil, err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, "", nil, err
		}
	default:
		ctype, text := "text/plain", m.Text
		if m.HTML != "" {
			ctype, text = "text/html", m.HTML
		}
		h.Set("Content-Type", ctype+"; charset=utf-8")
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		if err := writeQP(&body, text); err != nil {
			return nil, "", nil, err
		}
	}
	for _, k := range []string{"From", "To", "Cc", "Reply-To", "Subject", "Date", "Message-ID", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"} {
		if v := h.Get(k); v != "" {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	b.Write(body.Bytes())
	return b.Bytes(), fromAddr.Address, rcpts, nil
}

// address parses one address, refusing what could break out of its header.
func address(field, s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, fmt.Errorf("mail: %s %q holds a line break", field, s)
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return nil, fmt.Errorf("mail: %s %q: %w", field, s, err)
	}
	return a, nil
}

func writeQP(w io.Writer, text string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return err
	}
	return qp.Close()
}

func messageID(from string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	domain := "localhost"
	if i := strings.LastIndex(from, "@"); i >= 0 {
		domain = from[i+1:]
	}
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">"
}
