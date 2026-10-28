package gesso

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownload(t *testing.T) {
	file := Endpoint[slugIn, *Download]{Method: "GET", Pattern: "/files/{slug}"}
	modified := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	a := mustNew(t, Config{OpenAPI: &OpenAPIConfig{Title: "D", Version: "1"}}, moduleFunc(func(r *Router) {
		file.Handle(r, func(_ context.Context, in slugIn) (*Download, error) {
			if in.Slug == "missing" {
				return nil, ErrNotFound
			}
			return &Download{Body: bytes.NewReader([]byte("0123456789")), ContentType: "text/csv",
				Filename: "../rapor şubat.csv", ModTime: modified}, nil
		})
	}))
	w := send(a, "GET", "/files/x", nil)
	h := w.Header()
	if w.Code != 200 || w.Body.String() != "0123456789" || h.Get("Content-Type") != "text/csv" ||
		h.Get("Content-Disposition") != `attachment; filename*=utf-8''rapor%20%C5%9Fubat.csv` || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v %q", w.Code, h, w.Body)
	}
	if w := send(a, "GET", "/files/x", map[string]string{"Range": "bytes=2-4"}); w.Code != 206 || w.Body.String() != "234" {
		t.Fatalf("a range: %d %q", w.Code, w.Body)
	}
	if w := send(a, "GET", "/files/x", map[string]string{"If-Modified-Since": modified.Format(http.TimeFormat)}); w.Code != 304 {
		t.Fatalf("not modified: %d", w.Code)
	}
	if w := send(a, "GET", "/files/missing", nil); w.Code != 404 || !strings.Contains(w.Header().Get("Content-Type"), "problem") {
		t.Fatalf("an error is still a problem: %d %v", w.Code, w.Header())
	}
	srv := httptest.NewServer(a)
	defer srv.Close()
	for name, rm := range map[string]*Remote{"local": NewLocal(a), "remote": NewRemote(srv.URL, nil)} {
		d, err := file.Call(context.Background(), rm, slugIn{Slug: "x"})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(d.Body)
		_ = d.Body.(io.Closer).Close()
		if string(body) != "0123456789" || d.Filename != "rapor şubat.csv" || d.ContentType != "text/csv" {
			t.Fatalf("%s: %+v %q", name, d, body)
		}
		if _, err := file.Call(context.Background(), rm, slugIn{Slug: "missing"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !strings.Contains(string(a.OpenAPI()), `"application/octet-stream"`) {
		t.Fatal("OpenAPI does not say it is a file")
	}
}

type tick struct {
	N int `json:"n"`
}

func TestSSE(t *testing.T) {
	old := sseKeepAlive
	sseKeepAlive = 30 * time.Millisecond
	defer func() { sseKeepAlive = old }()
	stopped := make(chan struct{})
	watch := Endpoint[slugIn, *Stream]{Method: "GET", Pattern: "/watch/{slug}"}
	a := mustNew(t, Config{Logger: slog.New(slog.DiscardHandler), WriteTimeout: 200 * time.Millisecond}, moduleFunc(func(r *Router) {
		watch.Handle(r, func(_ context.Context, in slugIn) (*Stream, error) {
			return SSE(func(ctx context.Context, send func(tick) error) error {
				if in.Slug == "fail" {
					return errors.New("the feed broke")
				}
				for i := 1; i <= 3; i++ {
					if err := send(tick{N: i}); err != nil {
						return err
					}
					time.Sleep(150 * time.Millisecond) // past WriteTimeout in all
				}
				if in.Slug == "forever" {
					<-ctx.Done() // until the client goes
					close(stopped)
				}
				return nil
			}), nil
		})
	}))
	srv := httptest.NewUnstartedServer(a)
	srv.Config = a.server()
	srv.Config.Handler = a
	srv.Start()
	defer srv.Close()

	res, err := http.Get(srv.URL + "/watch/x")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(res.Body)
	res.Body.Close()
	got := string(all)
	if res.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(got, "data: {\"n\":1}\n\n") ||
		!strings.Contains(got, "data: {\"n\":3}\n\n") || !strings.Contains(got, ": ping") {
		t.Fatalf("%v\n%s", res.Header, got)
	}

	res, _ = http.Get(srv.URL + "/watch/fail")
	all, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(all), "event: error") || strings.Contains(string(all), "feed broke") {
		t.Fatalf("a failed stream says so, and not why: %s", all)
	}

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/watch/forever", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(res.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil || strings.Contains(line, `"n":3`) {
			break
		}
	}
	cancel()
	res.Body.Close()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream's ctx did not end when the client went")
	}
	if _, err := watch.Call(context.Background(), NewLocal(a), slugIn{Slug: "x"}); err == nil {
		t.Fatal("a stream is not called through an Endpoint")
	}
}
