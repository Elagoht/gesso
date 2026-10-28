package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"mime"
	"net"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// smtpServer is a mail server for the test: STARTTLS, AUTH PLAIN, one
// transaction kept.
type smtpServer struct {
	addr  string
	tls   *tls.Config
	roots *x509.CertPool

	mu       sync.Mutex
	from     string
	rcpts    []string
	data     string
	auth     string
	tlsOK    bool
	startTLS bool // offer STARTTLS
}

func newSMTPServer(t *testing.T, offerTLS bool) *smtpServer {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	ts.StartTLS()
	cert := ts.TLS.Certificates
	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	ts.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s := &smtpServer{addr: l.Addr().String(), tls: &tls.Config{Certificates: cert}, roots: roots, startTLS: offerTLS}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *smtpServer) serve(c net.Conn) {
	defer c.Close()
	r, w := bufio.NewReader(c), c
	say := func(line string) { _, _ = io.WriteString(w, line+"\r\n") }
	say("220 test")
	secure := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			if s.startTLS && !secure {
				say("250-test")
				say("250 STARTTLS")
			} else {
				say("250-test")
				say("250 AUTH PLAIN")
			}
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, s.tls)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, w, secure = tc, bufio.NewReader(tc), tc, true
			s.mu.Lock()
			s.tlsOK = true
			s.mu.Unlock()
		case "AUTH":
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			s.mu.Lock()
			s.auth = string(raw)
			s.mu.Unlock()
			say("235 ok")
		case "MAIL":
			s.mu.Lock()
			s.from = line
			s.mu.Unlock()
			say("250 ok")
		case "RCPT":
			s.mu.Lock()
			s.rcpts = append(s.rcpts, line)
			s.mu.Unlock()
			say("250 ok")
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 what")
		}
	}
}

func TestSMTPSends(t *testing.T) {
	srv := newSMTPServer(t, true)
	m := &SMTP{Addr: srv.addr, Username: "app", Password: "secret", From: "Uygulama <app@example.com>",
		TLS: &tls.Config{RootCAs: srv.roots, ServerName: "example.com"}}
	err := m.Send(context.Background(), Message{
		To: []string{"Ada <ada@example.com>"}, Cc: []string{"cc@example.com"}, Bcc: []string{"hidden@example.com"},
		Subject: "Şifre sıfırlama", Text: "Merhaba,\nbağlantı: https://x.example/r?t=1", HTML: "<p>Merhaba</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !srv.tlsOK || srv.auth != "\x00app\x00secret" {
		t.Fatalf("tls %v auth %q", srv.tlsOK, srv.auth)
	}
	if srv.from != "MAIL FROM:<app@example.com>" || len(srv.rcpts) != 3 || !strings.Contains(srv.rcpts[2], "hidden@example.com") {
		t.Fatalf("envelope %q %q", srv.from, srv.rcpts)
	}
	msg, err := mail.ReadMessage(strings.NewReader(srv.data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mimeDecoder).decode(msg.Header.Get("Subject"))
	if subject != "Şifre sıfırlama" || msg.Header.Get("Bcc") != "" || strings.Contains(srv.data, "hidden@") ||
		!strings.HasPrefix(msg.Header.Get("Content-Type"), "multipart/alternative") || msg.Header.Get("Message-Id") == "" {
		t.Fatalf("headers %v", msg.Header)
	}
	body, _ := io.ReadAll(msg.Body)
	if !strings.Contains(string(body), "text/html") || !strings.Contains(string(body), "Merhaba") {
		t.Fatalf("body %s", body)
	}
}

func TestSMTPRefusals(t *testing.T) {
	ctx := context.Background()
	plainOnly := newSMTPServer(t, false)
	m := &SMTP{Addr: plainOnly.addr, From: "a@example.com", TLS: &tls.Config{RootCAs: plainOnly.roots, ServerName: "example.com"}}
	if err := m.Send(ctx, Message{To: []string{"b@example.com"}, Text: "x"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("a server without STARTTLS: %v", err)
	}
	if err := (&SMTP{Addr: "mail.example.com:25", Security: Plain}).Send(ctx, Message{From: "a@example.com", To: []string{"b@example.com"}, Text: "x"}); err == nil || !strings.Contains(err.Error(), "clear text") {
		t.Fatalf("plain to another host: %v", err)
	}
	local := &SMTP{Addr: plainOnly.addr, Security: Plain, From: "a@example.com"}
	if err := local.Send(ctx, Message{To: []string{"b@example.com"}, Text: "x"}); err != nil {
		t.Fatalf("plain to this machine: %v", err)
	}
	for name, msg := range map[string]Message{
		"subject":   {To: []string{"b@example.com"}, Subject: "hi\r\nBcc: victim@example.com", Text: "x"},
		"recipient": {To: []string{"b@example.com\r\nBcc: victim@example.com"}, Text: "x"},
		"address":   {To: []string{"not an address"}, Text: "x"},
	} {
		if err := local.Send(ctx, msg); err == nil {
			t.Errorf("%s: a header injection or bad address was sent", name)
		}
	}
	slow, _ := net.Listen("tcp", "127.0.0.1:0")
	defer slow.Close()
	go func() {
		c, err := slow.Accept()
		if err == nil {
			time.Sleep(2 * time.Second)
			_ = c.Close()
		}
	}()
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := (&SMTP{Addr: slow.Addr().String(), Security: Plain}).Send(cctx, Message{From: "a@example.com", To: []string{"b@example.com"}, Text: "x"})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("a silent server held the send past its ctx: %v after %v", err, time.Since(start))
	}
}

type mimeDecoder struct{}

func (mimeDecoder) decode(s string) (string, error) {
	return new(mime.WordDecoder).DecodeHeader(s)
}
