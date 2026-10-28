// Package redis is a cache.Cache on Redis (or Valkey, KeyDB, Dragonfly): one
// cache every replica shares. It speaks the protocol itself (RESP2), with the
// standard library only, and passes cachetest.Run.
//
//	c, err := redis.New(redis.Options{Addr: "localhost:6379", Password: os.Getenv("REDIS_PASSWORD")})
//	defer c.Close()
package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/gesso/cache"
)

// Options configure the connection.
type Options struct {
	// Addr is "host:port". Required.
	Addr string
	// Username and Password authenticate (AUTH), when Password is set.
	Username, Password string
	// DB is the database number SELECTed.
	DB int
	// TLS, when set, speaks TLS (rediss).
	TLS *tls.Config
	// Prefix is put before every key: "myapp:" keeps them apart from another
	// app's in one Redis.
	Prefix string
	// PoolSize is how many connections are kept for reuse. Default 10.
	PoolSize int
	// DialTimeout bounds connecting. Default 5s.
	DialTimeout time.Duration
	// Timeout bounds one command when its ctx has no deadline. Default 3s.
	Timeout time.Duration
}

// Cache is Redis as a cache.Cache.
type Cache struct {
	opts Options
	pool chan *conn
	once sync.Once
	done chan struct{}
}

var _ cache.Cache = (*Cache)(nil)

// New checks opts, connects once to fail early, and returns the cache.
func New(opts Options) (*Cache, error) {
	if opts.Addr == "" {
		return nil, errors.New("redis: Options.Addr is required")
	}
	if opts.PoolSize <= 0 {
		opts.PoolSize = 10
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 5 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Second
	}
	c := &Cache{opts: opts, pool: make(chan *conn, opts.PoolSize), done: make(chan struct{})}
	cn, err := c.dial(context.Background())
	if err != nil {
		return nil, err
	}
	c.put(cn)
	return c, nil
}

// Close closes the kept connections.
func (c *Cache) Close() error {
	c.once.Do(func() { close(c.done) })
	for {
		select {
		case cn := <-c.pool:
			_ = cn.nc.Close()
		default:
			return nil
		}
	}
}

// Get returns the value under key.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	r, err := c.do(ctx, "GET", c.opts.Prefix+key)
	if err != nil {
		return nil, false, err
	}
	if r.null {
		return nil, false, nil
	}
	return r.bulk, true, nil
}

// Set stores value under key for ttl; zero or less keeps it.
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	args := []string{"SET", c.opts.Prefix + key, string(value)}
	if ttl > 0 {
		args = append(args, "PX", strconv.FormatInt(max(ttl.Milliseconds(), 1), 10))
	}
	_, err := c.do(ctx, args...)
	return err
}

// Delete removes key.
func (c *Cache) Delete(ctx context.Context, key string) error {
	_, err := c.do(ctx, "DEL", c.opts.Prefix+key)
	return err
}

// Ping checks the connection, for a readiness check.
func (c *Cache) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "PING")
	return err
}

// do runs one command on a pooled connection. A connection that failed —
// anything but an error reply — is closed, not pooled: what it would read
// next is unknown.
func (c *Cache) do(ctx context.Context, args ...string) (reply, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opts.Timeout)
		defer cancel()
	}
	cn, err := c.get(ctx)
	if err != nil {
		return reply{}, err
	}
	r, err := cn.command(ctx, args)
	var redisErr Error
	if err != nil && !errors.As(err, &redisErr) {
		_ = cn.nc.Close()
		return reply{}, err
	}
	c.put(cn)
	return r, err
}

func (c *Cache) get(ctx context.Context) (*conn, error) {
	select {
	case cn := <-c.pool:
		return cn, nil
	default:
		return c.dial(ctx)
	}
}

func (c *Cache) put(cn *conn) {
	select {
	case <-c.done:
		_ = cn.nc.Close()
	case c.pool <- cn:
	default:
		_ = cn.nc.Close() // the pool is full
	}
}

func (c *Cache) dial(ctx context.Context) (*conn, error) {
	d := &net.Dialer{Timeout: c.opts.DialTimeout}
	var nc net.Conn
	var err error
	if c.opts.TLS != nil {
		nc, err = (&tls.Dialer{NetDialer: d, Config: c.opts.TLS}).DialContext(ctx, "tcp", c.opts.Addr)
	} else {
		nc, err = d.DialContext(ctx, "tcp", c.opts.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	cn := &conn{nc: nc, r: bufio.NewReader(nc)}
	if c.opts.Password != "" {
		args := []string{"AUTH", c.opts.Password}
		if c.opts.Username != "" {
			args = []string{"AUTH", c.opts.Username, c.opts.Password}
		}
		if _, err := cn.command(ctx, args); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("redis: auth: %w", err)
		}
	}
	if c.opts.DB != 0 {
		if _, err := cn.command(ctx, []string{"SELECT", strconv.Itoa(c.opts.DB)}); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("redis: select: %w", err)
		}
	}
	return cn, nil
}

// Error is an error reply from Redis: the command failed, the connection is
// fine.
type Error string

func (e Error) Error() string { return "redis: " + string(e) }

type conn struct {
	nc net.Conn
	r  *bufio.Reader
}

type reply struct {
	null  bool
	bulk  []byte
	text  string
	n     int64
	items []reply
}

func (cn *conn) command(ctx context.Context, args []string) (reply, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = cn.nc.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = cn.nc.SetDeadline(time.Now()) })
	defer stop()
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	if _, err := io.WriteString(cn.nc, b.String()); err != nil {
		return reply{}, fmt.Errorf("redis: %w", err)
	}
	return cn.read()
}

const maxBulk = 512 << 20 // Redis's own limit on a string

func (cn *conn) read() (reply, error) {
	line, err := cn.r.ReadString('\n')
	if err != nil {
		return reply{}, fmt.Errorf("redis: %w", err)
	}
	if len(line) < 3 || !strings.HasSuffix(line, "\r\n") {
		return reply{}, errors.New("redis: a malformed reply")
	}
	kind, body := line[0], line[1:len(line)-2]
	switch kind {
	case '+':
		return reply{text: body}, nil
	case '-':
		return reply{}, Error(body)
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)
		if err != nil {
			return reply{}, errors.New("redis: a malformed integer")
		}
		return reply{n: n}, nil
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil || n > maxBulk {
			return reply{}, errors.New("redis: a malformed bulk length")
		}
		if n < 0 {
			return reply{null: true}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(cn.r, buf); err != nil {
			return reply{}, fmt.Errorf("redis: %w", err)
		}
		return reply{bulk: buf[:n]}, nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil || n > 1<<20 {
			return reply{}, errors.New("redis: a malformed array length")
		}
		if n < 0 {
			return reply{null: true}, nil
		}
		r := reply{items: make([]reply, 0, n)}
		for range n {
			item, err := cn.read()
			if err != nil {
				return reply{}, err
			}
			r.items = append(r.items, item)
		}
		return r, nil
	}
	return reply{}, fmt.Errorf("redis: an unknown reply %q", kind)
}
