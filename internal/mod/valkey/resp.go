package valkey

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal RESP2 client for the box's own admin calls (ACL,
// SCAN, INFO, BGSAVE...). Apps use their own clients (Bun.redis) with REDIS_URL.
type Client struct {
	c net.Conn
	r *bufio.Reader
}

// RedisError is an error reply from the server, e.g. "NOPERM ...".
type RedisError string

func (e RedisError) Error() string { return string(e) }

// ErrNil is returned by Do for a nil reply when a value was needed.
var ErrNil = errors.New("valkey: nil")

// Dial connects to network/addr ("unix", SocketPath) and authenticates.
func Dial(ctx context.Context, network, addr, user, pass string) (*Client, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	cl := &Client{c: c, r: bufio.NewReader(c)}
	if pass != "" {
		args := []string{"AUTH", pass}
		if user != "" {
			args = []string{"AUTH", user, pass}
		}
		if _, err := cl.Do(ctx, args...); err != nil {
			c.Close()
			return nil, err
		}
	}
	return cl, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.c.Close() }

// Do sends one command and reads its reply: string, int64, []any, nil, or a RedisError.
func (c *Client) Do(ctx context.Context, args ...string) (any, error) {
	dl := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = c.c.SetDeadline(dl)
	if _, err := io.WriteString(c.c, encodeCommand(args)); err != nil {
		return nil, err
	}
	v, err := c.read()
	if err != nil {
		return nil, err
	}
	if e, ok := v.(RedisError); ok {
		return nil, e
	}
	return v, nil
}

// Pipe sends several commands at once and reads their replies in order, one
// round trip for all of them. Error replies come back as RedisError values.
func (c *Client) Pipe(ctx context.Context, cmds ...[]string) ([]any, error) {
	if len(cmds) == 0 {
		return nil, nil
	}
	dl := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = c.c.SetDeadline(dl)
	var b strings.Builder
	for _, args := range cmds {
		b.WriteString(encodeCommand(args))
	}
	if _, err := io.WriteString(c.c, b.String()); err != nil {
		return nil, err
	}
	out := make([]any, len(cmds))
	for i := range out {
		v, err := c.read()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func encodeCommand(args []string) string {
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	return b.String()
}

// String runs a command expecting a string reply.
func (c *Client) String(ctx context.Context, args ...string) (string, error) {
	v, err := c.Do(ctx, args...)
	if err != nil {
		return "", err
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case nil:
		return "", ErrNil
	}
	return "", fmt.Errorf("valkey: unexpected reply %T", v)
}

// Int runs a command expecting an integer reply.
func (c *Client) Int(ctx context.Context, args ...string) (int64, error) {
	v, err := c.Do(ctx, args...)
	if err != nil {
		return 0, err
	}
	switch x := v.(type) {
	case int64:
		return x, nil
	case string:
		return strconv.ParseInt(x, 10, 64)
	case nil:
		return 0, ErrNil
	}
	return 0, fmt.Errorf("valkey: unexpected reply %T", v)
}

func (c *Client) read() (any, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" {
		return nil, errors.New("valkey: empty reply")
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return RedisError(body), nil
	case ':':
		return strconv.ParseInt(body, 10, 64)
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, nil
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = c.read(); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("valkey: unknown reply type %q", line[0])
}

// parseInfo turns an INFO reply into a map.
func parseInfo(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = v
		}
	}
	return out
}
