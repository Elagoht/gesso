package gesso

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"
)

// Download is a handler's answer that is a file, not JSON: an upload served
// back, an export.
//
//	func (m *Module) avatar(ctx context.Context, in IDIn) (*gesso.Download, error) {
//		obj, err := m.files.Get(ctx, key)
//		…
//		return &gesso.Download{Body: obj.Body, ContentType: "image/png", Size: obj.Size}, nil
//	}
//
// A Body that is an io.ReadSeeker with a ModTime is served with http.ServeContent:
// ranges and conditional requests answered. Body is closed when it is an
// io.Closer. Called through an Endpoint, a Download comes back with Body the
// response's: the caller closes it.
type Download struct {
	Body        io.Reader
	ContentType string // default application/octet-stream
	// Filename, when set, makes the answer an attachment by that name, which a
	// browser saves instead of showing — unless Inline.
	Filename string
	Inline   bool
	Size     int64     // the length, when known; 0 is unknown
	ModTime  time.Time // with a seekable Body, for If-Modified-Since
}

var (
	downloadType = reflect.TypeFor[*Download]()
	streamType   = reflect.TypeFor[*Stream]()
)

func (d *Download) write(w http.ResponseWriter, r *http.Request) {
	if c, ok := d.Body.(io.Closer); ok {
		defer c.Close()
	}
	h := w.Header()
	ctype := d.ContentType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	if d.Filename != "" || d.Inline {
		disposition := "attachment"
		if d.Inline {
			disposition = "inline"
		}
		params := map[string]string{}
		if d.Filename != "" {
			params["filename"] = cleanFilename(d.Filename)
		}
		h.Set("Content-Disposition", mime.FormatMediaType(disposition, params))
	}
	if rs, ok := d.Body.(io.ReadSeeker); ok && !d.ModTime.IsZero() {
		http.ServeContent(w, r, "", d.ModTime, rs)
		return
	}
	if d.Size > 0 {
		h.Set("Content-Length", strconv.FormatInt(d.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead && d.Body != nil {
		_, _ = io.Copy(w, d.Body)
	}
}

// downloadFrom is a Remote's answer as a Download: the body left open for the
// caller.
func downloadFrom(res *http.Response) *Download {
	d := &Download{Body: res.Body, ContentType: res.Header.Get("Content-Type"), Size: res.ContentLength}
	if d.Size < 0 {
		d.Size = 0
	}
	if disp, params, err := mime.ParseMediaType(res.Header.Get("Content-Disposition")); err == nil {
		d.Inline = disp == "inline"
		d.Filename = params["filename"]
	}
	return d
}

// Stream is a handler's answer that is a stream of server-sent events
// (text/event-stream), made with SSE.
type Stream struct {
	run func(w *sseWriter) error
}

// SSE streams events of type Ev: fn sends each with send, which writes it as a
// JSON data line and flushes it, and returns when the stream is over — or when
// ctx ends, which it does when the client goes. A comment line is sent every
// 15 seconds while fn is quiet, so proxies keep the connection.
//
//	func (m *Module) watch(ctx context.Context, in WatchIn) (*gesso.Stream, error) {
//		return gesso.SSE(func(ctx context.Context, send func(Note) error) error {
//			for n := range m.svc.Changes(ctx) {
//				if err := send(n); err != nil {
//					return err
//				}
//			}
//			return nil
//		}), nil
//	}
func SSE[Ev any](fn func(ctx context.Context, send func(Ev) error) error) *Stream {
	return &Stream{run: func(w *sseWriter) error {
		return fn(w.ctx, func(ev Ev) error {
			data, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			return w.send(data)
		})
	}}
}

type sseWriter struct {
	ctx context.Context
	mu  sync.Mutex
	w   http.ResponseWriter
	rc  *http.ResponseController
}

func (s *sseWriter) write(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(b); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sseWriter) send(data []byte) error {
	return s.write(append(append([]byte("data: "), data...), '\n', '\n'))
}

func (st *Stream) write(w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	rc := http.NewResponseController(w)
	// A stream lives longer than any answer should: the server's write
	// timeout is for answers.
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: do not hold the events back
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{ctx: r.Context(), w: w, rc: rc}
	if err := sw.write([]byte(": stream\n\n")); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	var pinger sync.WaitGroup
	defer func() {
		// The writer is the server's only until this handler returns: stop
		// the pings, and wait for one being written, first.
		cancel()
		pinger.Wait()
	}()
	sw.ctx = ctx
	pinger.Go(func() {
		t := time.NewTicker(sseKeepAlive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if sw.write([]byte(": ping\n\n")) != nil {
					cancel()
					return
				}
			}
		}
	})
	if err := st.run(sw); err != nil && ctx.Err() == nil {
		logger.ErrorContext(r.Context(), "stream failed", "path", r.URL.Path, "error", err,
			"request_id", RequestIDFrom(r.Context()))
		data, _ := json.Marshal(Problem{Status: 500, Code: "stream_failed", Title: "Internal Server Error"})
		_ = sw.write(append(append([]byte("event: error\ndata: "), data...), '\n', '\n'))
	}
}

var sseKeepAlive = 15 * time.Second
