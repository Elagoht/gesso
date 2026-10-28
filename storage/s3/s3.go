// Package s3 keeps files in an S3 bucket — AWS, or anything that speaks its
// API: MinIO, Cloudflare R2, Backblaze B2, Hetzner, DigitalOcean Spaces. It
// signs its requests itself (AWS Signature Version 4), with the standard
// library only, and passes storagetest.Run.
//
//	files, err := s3.New(s3.Options{
//		Endpoint:  "https://s3.eu-central-1.amazonaws.com",
//		Region:    "eu-central-1",
//		Bucket:    "my-app-uploads",
//		AccessKey: os.Getenv("S3_ACCESS_KEY"),
//		SecretKey: os.Getenv("S3_SECRET_KEY"),
//	})
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Elagoht/gesso/storage"
)

// Options configure a bucket.
type Options struct {
	// Endpoint is the service's base URL: "https://s3.eu-central-1.amazonaws.com",
	// "http://localhost:9000" for MinIO.
	Endpoint string
	// Region signs the requests: "eu-central-1"; "auto" for R2; MinIO takes
	// "us-east-1".
	Region string
	Bucket string
	// AccessKey and SecretKey are the credentials.
	AccessKey, SecretKey string
	// VirtualHost addresses the bucket as a host name (bucket.endpoint) rather
	// than a path (endpoint/bucket). AWS prefers it; MinIO wants a path.
	VirtualHost bool
	// Prefix is put before every key: "uploads/" keeps them in a folder of a
	// bucket others share.
	Prefix string
	// PageSize is how many keys one List request asks for. Default 1000, the
	// most S3 gives.
	PageSize int
	// HTTPClient sends the requests. Default http.DefaultClient.
	HTTPClient *http.Client
	// Clock tells the time requests are signed at. Default time.Now.
	Clock func() time.Time
}

// Bucket is an S3 bucket as a storage.Storage.
type Bucket struct {
	opts Options
	base *url.URL
}

var _ storage.Storage = (*Bucket)(nil)

// New checks opts and returns the bucket.
func New(opts Options) (*Bucket, error) {
	if opts.Endpoint == "" || opts.Region == "" || opts.Bucket == "" || opts.AccessKey == "" || opts.SecretKey == "" {
		return nil, errors.New("s3: Endpoint, Region, Bucket, AccessKey and SecretKey are required")
	}
	u, err := url.Parse(strings.TrimSuffix(opts.Endpoint, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("s3: Endpoint %q: want http(s)://host", opts.Endpoint)
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Bucket{opts: opts, base: u}, nil
}

// spoolMemory is how much of a Put is kept in memory before it goes to a
// temporary file: S3 wants a body's length before it starts.
const spoolMemory = 8 << 20

// Put stores what r reads under key. It reads all of it first — to a
// temporary file past 8 MiB — so a reader that fails leaves the old object.
func (b *Bucket) Put(ctx context.Context, key string, r io.Reader) (storage.Object, error) {
	if err := storage.ValidKey(key); err != nil {
		return storage.Object{}, err
	}
	var mem bytes.Buffer
	n, err := io.CopyN(&mem, r, spoolMemory+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return storage.Object{}, err
	}
	var body io.ReadSeeker = bytes.NewReader(mem.Bytes())
	size := n
	if n > spoolMemory {
		f, err := os.CreateTemp("", "gesso-s3-*")
		if err != nil {
			return storage.Object{}, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, err := f.Write(mem.Bytes()); err != nil {
			return storage.Object{}, err
		}
		rest, err := io.Copy(f, r)
		if err != nil {
			return storage.Object{}, err
		}
		size += rest
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return storage.Object{}, err
		}
		body = f
	}
	res, err := b.do(ctx, http.MethodPut, key, nil, body, size)
	if err != nil {
		return storage.Object{}, err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return storage.Object{}, statusError("put", key, res)
	}
	return storage.Object{Key: key, Size: size, ModTime: b.opts.Clock().UTC().Truncate(time.Second)}, nil
}

// Open reads the object under key.
func (b *Bucket) Open(ctx context.Context, key string) (io.ReadCloser, storage.Object, error) {
	if err := storage.ValidKey(key); err != nil {
		return nil, storage.Object{}, err
	}
	res, err := b.do(ctx, http.MethodGet, key, nil, nil, 0)
	if err != nil {
		return nil, storage.Object{}, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, storage.Object{}, statusError("open", key, res)
	}
	return res.Body, object(key, res), nil
}

// Stat describes the object under key.
func (b *Bucket) Stat(ctx context.Context, key string) (storage.Object, error) {
	if err := storage.ValidKey(key); err != nil {
		return storage.Object{}, err
	}
	res, err := b.do(ctx, http.MethodHead, key, nil, nil, 0)
	if err != nil {
		return storage.Object{}, err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return storage.Object{}, statusError("stat", key, res)
	}
	return object(key, res), nil
}

// Delete removes the object under key; a missing one is not an error.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	if err := storage.ValidKey(key); err != nil {
		return err
	}
	res, err := b.do(ctx, http.MethodDelete, key, nil, nil, 0)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNotFound {
		return statusError("delete", key, res)
	}
	return nil
}

type listResult struct {
	Contents []struct {
		Key          string    `xml:"Key"`
		Size         int64     `xml:"Size"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// List describes the objects whose keys begin with prefix, by key.
func (b *Bucket) List(ctx context.Context, prefix string) ([]storage.Object, error) {
	var out []storage.Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {b.opts.Prefix + prefix}}
		if b.opts.PageSize > 0 {
			q.Set("max-keys", strconv.Itoa(b.opts.PageSize))
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		res, err := b.do(ctx, http.MethodGet, "", q, nil, 0)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			return nil, statusError("list", prefix, res)
		}
		var page listResult
		err = xml.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&page)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: list %q: %w", prefix, err)
		}
		for _, c := range page.Contents {
			key := strings.TrimPrefix(c.Key, b.opts.Prefix)
			if storage.ValidKey(key) != nil {
				continue // not one of ours: a "folder" marker, a hidden file
			}
			out = append(out, storage.Object{Key: key, Size: c.Size, ModTime: c.LastModified.UTC()})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			break
		}
		token = page.NextContinuationToken
	}
	slices.SortFunc(out, func(a, c storage.Object) int { return strings.Compare(a.Key, c.Key) })
	return out, nil
}

func object(key string, res *http.Response) storage.Object {
	o := storage.Object{Key: key, Size: res.ContentLength}
	if t, err := http.ParseTime(res.Header.Get("Last-Modified")); err == nil {
		o.ModTime = t.UTC()
	}
	return o
}

func statusError(op, key string, res *http.Response) error {
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %q", storage.ErrNotFound, key)
	}
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&e)
	return fmt.Errorf("s3: %s %q: %s %s %s", op, key, res.Status, e.Code, e.Message)
}

// do sends a signed request for key (the bucket itself when key is empty).
func (b *Bucket) do(ctx context.Context, method, key string, query url.Values, body io.ReadSeeker, size int64) (*http.Response, error) {
	u := *b.base
	objectPath := ""
	if key != "" {
		objectPath = "/" + b.opts.Prefix + key
	}
	if b.opts.VirtualHost {
		u.Host = b.opts.Bucket + "." + u.Host
		u.Path = objectPath
		if u.Path == "" {
			u.Path = "/"
		}
	} else {
		u.Path = "/" + b.opts.Bucket + objectPath
	}
	u.RawPath = escapePath(u.Path)
	u.RawQuery = canonicalQuery(query)
	var rdr io.Reader
	if body != nil {
		rdr = body
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		req.GetBody = func() (io.ReadCloser, error) {
			if _, err := body.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			return io.NopCloser(body), nil
		}
	}
	sign(req, b.opts.AccessKey, b.opts.SecretKey, b.opts.Region, b.opts.Clock())
	return b.opts.HTTPClient.Do(req)
}

// sign adds AWS Signature Version 4 to req, the payload unsigned (its hash
// would mean reading it twice; TLS keeps it whole).
func sign(req *http.Request, accessKey, secretKey, region string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	payload := req.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		payload = "UNSIGNED-PAYLOAD"
		req.Header.Set("X-Amz-Content-Sha256", payload)
	}
	req.Header.Set("X-Amz-Date", amzDate)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	names := []string{"host"}
	values := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || (!strings.HasPrefix(lk, "x-amz-") && lk != "range" && lk != "content-type" && lk != "content-md5") {
			continue
		}
		names = append(names, lk)
		values[lk] = strings.Join(trimAll(v), ",")
	}
	slices.Sort(names)
	var canonicalHeaders strings.Builder
	for _, n := range names {
		canonicalHeaders.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaders.String(),
		signed,
		payload,
	}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+secretKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+
		", SignedHeaders="+signed+", Signature="+signature)
}

func trimAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return out
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// escapePath is a path as SigV4 wants it: every byte but unreserved ones and
// "/" percent-encoded.
func escapePath(p string) string {
	var b strings.Builder
	for i := range len(p) {
		c := p[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' || c == '/' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// canonicalQuery is q sorted and encoded as SigV4 wants it.
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		vs := slices.Clone(q[k])
		slices.Sort(vs)
		for _, v := range vs {
			parts = append(parts, escapeQuery(k)+"="+escapeQuery(v))
		}
	}
	return strings.Join(parts, "&")
}

func escapeQuery(s string) string { return strings.ReplaceAll(escapePath(s), "/", "%2F") }
