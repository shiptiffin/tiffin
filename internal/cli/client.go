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
	"strconv"
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
func openBox(ctx context.Context, home string, extra ...func(*api.Deps)) (b *box, newOwner string, err error) {
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
	deps := api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Version: version.Version}
	for _, f := range extra {
		f(&deps)
	}
	a := api.New(deps)
	return &box{db: db, tokens: tm, api: a, home: home}, secret, nil
}

func (b *box) Close() error { return b.db.Close() }

// readOwnerToken reads the owner token file. A process that lost the race to
// bootstrap a fresh box may get here before the winner has written the file,
// so it waits briefly for it to appear.
func readOwnerToken(home string) string {
	for i := 0; i < 40; i++ {
		raw, err := os.ReadFile(filepath.Join(home, ownerTokenFile))
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			return strings.TrimSpace(string(raw))
		}
		time.Sleep(50 * time.Millisecond)
	}
	return ""
}

const agentTokenFile = "agent-token"

// agentToken returns the box's local agent token, minting (or re-minting
// once expired, revoked or narrower) a 90-day key with full access to all projects.
func (b *box) agentToken(ctx context.Context) (string, error) {
	path := filepath.Join(b.home, agentTokenFile)
	if raw, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(raw))
		// Older versions minted a narrower token; the agent key now has full
		// access to all projects, so replace it.
		if p, err := b.tokens.Authenticate(ctx, s); err == nil && p.BoxAdmin() {
			return s, nil
		}
	}
	owner, err := b.tokens.Authenticate(ctx, readOwnerToken(b.home))
	if err != nil {
		return "", fmt.Errorf("cannot mint the local agent key: owner token unavailable (%v); set TIFFIN_TOKEN", err)
	}
	secret, _, err := b.tokens.CreateKey(ctx, owner, tokens.KeyRequest{Name: "local-agent", Projects: tokens.Projects{tokens.AllProjects}, Access: tokens.LevelFull, TTL: 90 * 24 * time.Hour})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}

// client talks to the API: in-process for a local box, over HTTP otherwise.
type client struct {
	handler   http.Handler // local
	base      string       // remote
	transport http.RoundTripper
	token     string
	session   string
	model     string
	close     func() error
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
		return &client{base: strings.TrimRight(a.url, "/"), token: a.token, session: a.session, model: a.model, close: func() error { return nil }}, nil
	}
	// A box set up with `tiffin up` is the default target, unless a local
	// home was asked for explicitly.
	if !a.homeExplicit {
		if _, bx := a.currentBox(); bx != nil {
			tr, err := boxTransport(bx.CAFile)
			if err != nil {
				return nil, err
			}
			tok := orDefault(a.token, bx.Token)
			if a.token == "" && bx.AgentToken != "" && a.agentRun() {
				// An agent's shell acts as the box's agent key, as `tiffin mcp` does,
				// so History shows the agent rather than the owner.
				tok = bx.AgentToken
			}
			return &client{base: strings.TrimRight(bx.URL, "/"), token: tok, session: a.session, model: a.model, transport: tr, close: func() error { return nil }}, nil
		}
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
	return &client{handler: b.api.Handler(), token: tok, session: a.session, model: a.model, close: b.Close}, nil
}

// agentRun reports whether a coding agent runs this command: Claude Code
// sets CLAUDECODE=1 in its shells; any other agent can set TIFFIN_AGENT=1.
func (a *app) agentRun() bool {
	return a.io.Env("TIFFIN_AGENT") == "1" || a.io.Env("CLAUDECODE") == "1"
}

// do performs one API call and returns the status and raw body.
func (c *client) do(ctx context.Context, method, path string, q url.Values, body any) (int, []byte, error) {
	wait := requestTimeout(q.Get("timeoutSeconds"))
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		var asked struct {
			TimeoutSeconds json.Number `json:"timeoutSeconds"`
		}
		if json.Unmarshal(b, &asked) == nil {
			wait = max(wait, requestTimeout(asked.TimeoutSeconds.String()))
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
	hc := &http.Client{Timeout: wait, Transport: c.transport}
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
	if c.model != "" {
		h.Set(api.ModelHeader, c.model)
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

// requestTimeout is how long a call may take: a minute, or longer when the
// call itself asks the box to wait longer (a query's timeoutSeconds).
func requestTimeout(timeoutSeconds string) time.Duration {
	n, err := strconv.Atoi(timeoutSeconds)
	if err != nil || n <= 0 {
		return 60 * time.Second
	}
	return max(60*time.Second, time.Duration(n)*time.Second+15*time.Second)
}
