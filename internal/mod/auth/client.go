package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// engine talks to the auth engine's admin API over its unix socket.
type engine struct {
	hc *http.Client
}

// newEngine returns a client for the admin socket at path.
func newEngine(path string) *engine {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &engine{hc: &http.Client{Transport: tr, Timeout: 5 * time.Minute}}
}

// defaultEngine is the box's engine; tests swap it.
var defaultEngine = newEngine(AdminSocket)

// EngineError is an error the engine answered with.
type EngineError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *EngineError) Error() string {
	return fmt.Sprintf("auth engine: %s (%d %s)", e.Message, e.Status, e.Code)
}

// ErrEngineDown means the engine isn't answering on its socket.
var ErrEngineDown = errors.New("the auth engine isn't running (systemd unit tiffin-auth); run `tiffin provision` on the box, or check `journalctl -u tiffin-auth`")

func (e *engine) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := "http://engine" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := e.hc.Do(req)
	if err != nil {
		var ne net.Error
		var oe *net.OpError
		if errors.As(err, &oe) || errors.As(err, &ne) {
			return fmt.Errorf("%w: %v", ErrEngineDown, err)
		}
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		ee := &EngineError{Status: res.StatusCode}
		if json.Unmarshal(raw, ee) != nil || ee.Message == "" {
			ee.Message = string(raw)
		}
		return ee
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// MigrateResult is what a migration did.
type MigrateResult struct {
	Created []string `json:"created"`
	Added   []string `json:"added"`
	Ms      int64    `json:"ms"`
}

func (e *engine) reload(ctx context.Context) error {
	return e.do(ctx, http.MethodPost, "/reload", nil, nil, nil)
}

func (e *engine) migrate(ctx context.Context, project string) (*MigrateResult, error) {
	var out MigrateResult
	return &out, e.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(project)+"/migrate", nil, nil, &out)
}

func (e *engine) drop(ctx context.Context, project string) error {
	return e.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(project)+"/drop", url.Values{"confirm": {project}}, nil, nil)
}
