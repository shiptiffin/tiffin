package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/btahir/tiffin/internal/version"
)

// box is a local Tiffin box: state on disk plus the API over it.
type box struct {
	db     *state.DB
	tokens *tokens.Manager
	api    *api.API
	home   string
}

const ownerTokenFile = "owner-token"

// openBox opens the box in home, bootstrapping the owner token on first use
// (written to home/owner-token, mode 0600). newOwner is the fresh secret, if any.
func openBox(ctx context.Context, home string) (b *box, newOwner string, err error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, "", err
	}
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		return nil, "", err
	}
	tm := tokens.NewManager(db)
	secret, created, err := tm.Bootstrap(ctx)
	if err != nil {
		db.Close()
		return nil, "", err
	}
	if created {
		if err := os.WriteFile(filepath.Join(home, ownerTokenFile), []byte(secret+"\n"), 0o600); err != nil {
			db.Close()
			return nil, "", err
		}
	}
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Version: version.Version})
	return &box{db: db, tokens: tm, api: a, home: home}, secret, nil
}

func (b *box) Close() error { return b.db.Close() }

func readOwnerToken(home string) string {
	raw, err := os.ReadFile(filepath.Join(home, ownerTokenFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// client talks to the API: in-process for a local box, over HTTP otherwise.
type client struct {
	handler http.Handler // local
	base    string       // remote
	token   string
	session string
	close   func() error
}

func (a *app) client(ctx context.Context) (*client, error) {
	if a.url != "" {
		u, err := url.Parse(a.url)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, &exitError{ExitInvalid, "--url must be an http(s) URL"}
		}
		if a.token == "" {
			return nil, &exitError{ExitAuth, "TIFFIN_TOKEN is not set (needed with TIFFIN_URL)"}
		}
		return &client{base: strings.TrimRight(a.url, "/"), token: a.token, session: a.session, close: func() error { return nil }}, nil
	}
	b, fresh, err := openBox(ctx, a.home)
	if err != nil {
		return nil, fmt.Errorf("open local box in %s: %w", a.home, err)
	}
	if fresh != "" && a.tty() {
		fmt.Fprintf(a.io.Err, "Created a local box in %s. Owner token saved to %s.\n", a.home, filepath.Join(a.home, ownerTokenFile))
	}
	tok := a.token
	if tok == "" {
		tok = readOwnerToken(a.home)
	}
	return &client{handler: b.api.Handler(), token: tok, session: a.session, close: b.Close}, nil
}

// do performs one API call and returns the status and raw body.
func (c *client) do(ctx context.Context, method, path string, q url.Values, body any) (int, []byte, error) {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	if c.handler != nil {
		req := httptest.NewRequestWithContext(ctx, method, path, rd)
		c.headers(req.Header, body != nil)
		rec := httptest.NewRecorder()
		c.handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes(), nil
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	c.headers(req.Header, body != nil)
	hc := &http.Client{Timeout: 60 * time.Second}
	res, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, nil, fmt.Errorf("cannot reach %s: %v", c.base, ue.Err)
		}
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	return res.StatusCode, raw, err
}

func (c *client) headers(h http.Header, hasBody bool) {
	if hasBody {
		h.Set("Content-Type", "application/json")
	}
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "tiffin-cli/"+version.Version)
	if c.token != "" {
		h.Set("Authorization", "Bearer "+c.token)
	}
	if c.session != "" {
		h.Set(api.SessionHeader, c.session)
	}
}

// exitFor maps an HTTP status (and problem code) to the CLI exit code.
func exitFor(status int, raw []byte) int {
	switch {
	case status >= 200 && status < 300:
		return ExitOK
	case status == 401 || status == 403:
		return ExitAuth
	case status == 400 || status == 422:
		return ExitInvalid
	case status == 428:
		return ExitConfirm
	case status == 409:
		var p struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Code == "conflict" {
			return ExitConfirm // re-plan and confirm again
		}
	}
	return ExitError
}
