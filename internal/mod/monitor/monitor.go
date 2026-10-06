// Package monitor is the box's heartbeat to someone outside. A box cannot
// report its own death, so once the owner sets a ping URL the box pings it
// about once a minute and an outside service (healthchecks.io, an Uptime
// Kuma push monitor, or `tiffin watch`) tells the owner when the pings stop.
// When the box's own status checks have failed for ten minutes the ping
// turns into a failure ping (healthchecks' <url>/fail, Kuma's status=down).
//
// A ping carries a small status summary: version, uptime and how many
// checks fail, by name. Project names and what a failing check says go only
// when the owner opts in (details), and nothing secret ever does.
package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

func init() { platform.Register(&Module{}) }

const (
	// Every is how often the box pings, give or take Jitter, so boxes that
	// started together do not ping in the same second.
	Every  = time.Minute
	Jitter = 10 * time.Second
	// FailAfter is how long the box's checks must fail before pings say so.
	FailAfter = 10 * time.Minute

	kvNS, kvKey = "box", "monitor"
	timeout     = 10 * time.Second
	detailMax   = 200
)

// Kinds of ping.
const (
	KindOK    = "ok"
	KindFail  = "fail"
	KindStart = "start"
)

// Config is the stored setting.
type Config struct {
	URL     string    `json:"url"`
	Details bool      `json:"details,omitempty"`
	SetAt   time.Time `json:"setAt"`
	SetBy   string    `json:"setBy,omitempty"`
}

// Payload is what a ping carries (the request body; Kuma gets a one-line
// summary instead). It never holds a secret or the ping URL.
type Payload struct {
	Status        string            `json:"status" enum:"ok,failing" doc:"failing once a check has failed for 10 minutes"`
	Version       string            `json:"version"`
	UptimeSeconds int64             `json:"uptimeSeconds" doc:"Since Tiffin started"`
	Checks        int               `json:"checks" doc:"How many status checks ran"`
	FailedChecks  int               `json:"failedChecks"`
	Failing       []string          `json:"failing,omitempty" doc:"Names of the failing checks (disk, postgres...)"`
	Details       map[string]string `json:"details,omitempty" doc:"What each failing check says; only with details on"`
	Projects      []string          `json:"projects,omitempty" doc:"The box's projects; only with details on"`
}

// Beat is one ping the box sent.
type Beat struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind" enum:"ok,fail,start"`
	OK     bool      `json:"ok" doc:"The receiver answered 2xx"`
	Status int       `json:"status,omitempty" doc:"HTTP status the receiver answered"`
	Error  string    `json:"error,omitempty"`
	Ms     int64     `json:"ms"`
}

// Module implements the outside heartbeat.
type Module struct {
	client  *http.Client
	now     func() time.Time
	mu      sync.Mutex
	started time.Time
	// badSince is when the checks started failing (zero while they pass).
	badSince time.Time
	last     *Beat
}

func (*Module) Name() string { return "monitor" }

// Order: last, after every module whose checks it reports.
func (*Module) Order() int { return 90 }

// Start pings /start once, then about once a minute.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	m.mu.Lock()
	m.started = m.clock()
	m.mu.Unlock()
	go func() {
		sleep(ctx, rand.N(Jitter))
		kind := KindStart
		for ctx.Err() == nil {
			if cfg, err := load(ctx, p); err == nil && cfg != nil {
				m.ping(ctx, p, cfg, kind)
				kind = ""
			}
			sleep(ctx, nextDelay(rand.N(2*Jitter)))
		}
	}()
	return nil
}

// nextDelay is the wait before the next ping: Every, shifted by up to
// Jitter either way (r is in [0, 2*Jitter)).
func nextDelay(r time.Duration) time.Duration { return Every - Jitter + r }

// kindFor says whether a ping is a plain one or a failure, from when the
// checks started failing.
func kindFor(now, badSince time.Time) string {
	if !badSince.IsZero() && now.Sub(badSince) >= FailAfter {
		return KindFail
	}
	return KindOK
}

func (m *Module) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// ping sends one ping of the given kind ("" for ok or fail by the checks)
// and remembers it.
func (m *Module) ping(ctx context.Context, p *platform.Platform, cfg *Config, kind string) Beat {
	pl := m.payload(ctx, p, cfg.Details)
	if kind == "" {
		kind = KindOK
		if pl.Status == "failing" {
			kind = KindFail
		}
	}
	b := send(ctx, m.httpClient(), cfg.URL, kind, pl)
	b.At = m.clock()
	m.mu.Lock()
	m.last = &b
	m.mu.Unlock()
	return b
}

// payload runs the box's checks and summarises them, tracking how long
// they have been failing.
func (m *Module) payload(ctx context.Context, p *platform.Platform, details bool) Payload {
	checks := boxChecks(ctx, p)
	now := m.clock()
	m.mu.Lock()
	started := m.started
	pl := summarise(checks, details)
	switch {
	case pl.FailedChecks == 0:
		m.badSince = time.Time{}
	case m.badSince.IsZero():
		m.badSince = now
	}
	if kindFor(now, m.badSince) == KindFail {
		pl.Status = "failing"
	}
	m.mu.Unlock()
	if started.IsZero() {
		started = now
	}
	pl.Version, pl.UptimeSeconds = p.Version, int64(now.Sub(started).Seconds())
	if pl.Version == "" {
		pl.Version = "dev"
	}
	if details && p.DB != nil {
		if prs, err := p.DB.ListProjects(ctx); err == nil {
			pl.Projects = prs
		}
	}
	return pl
}

// summarise counts the checks. Only their names go out unless details is on.
func summarise(checks []platform.Check, details bool) Payload {
	pl := Payload{Status: "ok", Checks: len(checks)}
	for _, c := range checks {
		if c.OK {
			continue
		}
		pl.FailedChecks++
		pl.Failing = append(pl.Failing, c.Name)
		if details {
			if pl.Details == nil {
				pl.Details = map[string]string{}
			}
			d := c.Detail
			if len(d) > detailMax {
				d = d[:detailMax] + "…"
			}
			pl.Details[c.Name] = d
		}
	}
	return pl
}

// boxChecks are the checks /v1/status reports.
func boxChecks(ctx context.Context, p *platform.Platform) []platform.Check {
	if p.BoxChecks != nil {
		return p.BoxChecks(ctx)
	}
	return p.Checks(ctx)
}

func (m *Module) httpClient() *http.Client {
	if m.client != nil {
		return m.client
	}
	return &http.Client{Timeout: timeout}
}

// Receiver names the kind of service a ping URL belongs to.
func Receiver(u *url.URL) string {
	if strings.Contains(u.Path, "/api/push/") {
		return "uptime-kuma"
	}
	return "healthchecks"
}

// pingRequest builds the request for one ping. Healthchecks-style receivers
// (healthchecks.io, `tiffin watch`) get a POST to <url>, <url>/fail or
// <url>/start with the payload as JSON; an Uptime Kuma push URL gets a GET
// with status=up|down and a one-line msg.
func pingRequest(ctx context.Context, raw, kind string, pl Payload) (*http.Request, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if Receiver(u) == "uptime-kuma" {
		q := u.Query()
		q.Set("status", "up")
		if kind == KindFail {
			q.Set("status", "down")
		}
		q.Set("msg", oneLine(pl))
		u.RawQuery = q.Encode()
		return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	}
	if kind != KindOK {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + kind
		u.RawPath = ""
	}
	body, err := json.Marshal(pl)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}

// oneLine is the payload as a short sentence (Kuma shows it as the message).
func oneLine(pl Payload) string {
	s := fmt.Sprintf("%s: tiffin %s, up %s, %d of %d checks failing", pl.Status, pl.Version,
		(time.Duration(pl.UptimeSeconds) * time.Second).String(), pl.FailedChecks, pl.Checks)
	if len(pl.Failing) > 0 {
		s += " (" + strings.Join(pl.Failing, ", ") + ")"
	}
	return s
}

// send delivers one ping.
func send(ctx context.Context, c *http.Client, raw, kind string, pl Payload) Beat {
	b := Beat{Kind: kind}
	t := time.Now()
	req, err := pingRequest(ctx, raw, kind, pl)
	if err == nil {
		req.Header.Set("User-Agent", "tiffin/"+pl.Version)
		var res *http.Response
		if res, err = c.Do(req); err == nil {
			res.Body.Close()
			b.Status = res.StatusCode
			if res.StatusCode/100 != 2 {
				err = fmt.Errorf("the receiver answered %s", res.Status)
			}
		}
	}
	b.Ms = time.Since(t).Milliseconds()
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL may hold the check's secret; the host is enough
		}
		b.Error = err.Error()
	}
	b.OK = err == nil
	return b
}

func load(ctx context.Context, p *platform.Platform) (*Config, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, kvKey)
	if err != nil || !ok {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil || c.URL == "" {
		return nil, err
	}
	return &c, nil
}

func save(ctx context.Context, p *platform.Platform, c *Config) error {
	if c == nil {
		return p.DB.KVDelete(ctx, kvNS, kvKey)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return p.DB.KVPut(ctx, kvNS, kvKey, raw)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
