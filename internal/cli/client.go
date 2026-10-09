package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/btahir/tiffin/internal/platform"
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

// nameOwner gives a managed box's owner the name its setup brought (the
// name on their ShipTiffin account), once and only over the starting
// "Owner". A box without one, or set up with `tiffin up`, is left alone; a
// failure only costs the name, which the dashboard then asks for.
func nameOwner(ctx context.Context, tm *tokens.Manager, errw io.Writer) {
	c, err := platform.LoadManagedConfig()
	if err != nil || c == nil || c.OwnerName == "" {
		return
	}
	if _, err := tm.NameOwner(ctx, c.OwnerName); err != nil {
		fmt.Fprintln(errw, "owner name:", err)
	}
}

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
	note      func(string) // says when a request is sent again (nil: quiet)
}

// notes is where a client says it sends a request again: stderr on a
// terminal, or with TIFFIN_DEBUG=1.
func (a *app) notes() func(string) {
	if !a.tty() && a.io.Env("TIFFIN_DEBUG") != "1" {
		return nil
	}
	return func(s string) { fmt.Fprintln(a.io.Err, a.paint("· "+s, dim)) }
}

// roundTripper is the client's transport, sending a request again when a
// dropped connection allows it (see retryTransport).
func (c *client) roundTripper() http.RoundTripper { return newRetryTransport(c.transport, c.note) }

func (a *app) client(ctx context.Context) (*client, error) {
	if a.url != "" {
		u, err := url.Parse(a.url)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, &exitError{ExitInvalid, "--url must be an http(s) URL"}
		}
		if a.token == "" {
			return nil, &exitError{ExitAuth, "TIFFIN_TOKEN is not set (needed with TIFFIN_URL)"}
		}
		return &client{base: strings.TrimRight(a.url, "/"), token: a.token, session: a.session, model: a.model, close: func() error { return nil }, note: a.notes()}, nil
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
			return &client{base: strings.TrimRight(bx.URL, "/"), token: tok, session: a.session, model: a.model, transport: tr, close: func() error { return nil }, note: a.notes()}, nil
		}
	}
	if a.token != "" && !a.homeExplicit {
		if _, err := os.Stat(filepath.Join(a.home, "state.db")); errors.Is(err, fs.ErrNotExist) {
			// A key with nowhere to go: without this the CLI would make an
			// empty local box and answer 401, which says nothing useful.
			return nil, &exitError{ExitInvalid, "TIFFIN_TOKEN is set, but this computer knows no box to use it with: set TIFFIN_URL to the box's dashboard address (https://dashboard.<the box's domain>), or make a box with tiffin up"}
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
	return c.send(ctx, method, path, q, body, "")
}

// doOnce is do for a create the box must do only once (a duplicate, an
// export, an import): it sends an Idempotency-Key, so when the connection
// drops before the answer, the client asks the box what became of it, or
// sends it again, and the box never does it twice.
func (c *client) doOnce(ctx context.Context, method, path string, q url.Values, body any) (int, []byte, error) {
	return c.send(ctx, method, path, q, body, newIdempotencyKey())
}

func (c *client) send(ctx context.Context, method, path string, q url.Values, body any, key string) (int, []byte, error) {
	// A call that asks the box to hold the answer (timeoutSeconds, or a
	// deploy's wait) gets that long plus a margin, not the default minute.
	wait := max(requestTimeout(q.Get("timeoutSeconds")), requestTimeout(q.Get("wait")))
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return 0, nil, err
		}
		var asked struct {
			TimeoutSeconds json.Number `json:"timeoutSeconds"`
		}
		if json.Unmarshal(b, &asked) == nil {
			wait = max(wait, requestTimeout(asked.TimeoutSeconds.String()))
		}
	}
	reader := func() io.Reader {
		if body == nil {
			return nil
		}
		return bytes.NewReader(b) // NewRequest gives it a GetBody: it can be sent again
	}
	if c.handler != nil {
		req := httptest.NewRequestWithContext(ctx, method, path, reader())
		c.headers(req.Header, body != nil)
		if key != "" {
			req.Header.Set(api.IdempotencyHeader, key)
		}
		rec := httptest.NewRecorder()
		c.handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes(), nil
	}
	hc := &http.Client{Timeout: wait, Transport: c.roundTripper()}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader())
		if err != nil {
			return 0, nil, err
		}
		c.headers(req.Header, body != nil)
		if key != "" {
			req.Header.Set(api.IdempotencyHeader, key)
		}
		res, err := hc.Do(req)
		if err != nil {
			return 0, nil, c.unreachable(err)
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
		res.Body.Close()
		if err != nil && attempt < len(retryWaits) && (idempotentMethod(method) || key != "") && classify(err) != fatal && ctx.Err() == nil {
			// The answer broke off: ask again (a keyed create gets its stored answer).
			if c.note != nil {
				c.note(retryNote)
			}
			if sleepCtx(ctx, retryWaits[attempt]) == nil {
				continue
			}
		}
		if err != nil {
			return res.StatusCode, raw, c.unreachable(err)
		}
		return res.StatusCode, raw, nil
	}
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
// call itself asks the box to wait longer (a query's timeoutSeconds or wait,
// in seconds).
func requestTimeout(timeoutSeconds string) time.Duration {
	n, err := strconv.Atoi(timeoutSeconds)
	if err != nil || n <= 0 {
		return 60 * time.Second
	}
	return max(60*time.Second, time.Duration(n)*time.Second+15*time.Second)
}
