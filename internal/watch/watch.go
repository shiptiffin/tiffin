// Package watch is a small outside checker for Tiffin boxes (`tiffin
// watch`), run on another machine. It checks each box's public /v1/health,
// collects the heartbeats boxes send (`tiffin monitor set
// <collector>/ping/<token>`), and when a box is down for a while (health
// failing, heartbeats missing, or the box pinging /fail) it alerts through a
// webhook and/or a command, once when the box goes down and once when it
// comes back.
package watch

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config is the watch file (JSON).
type Config struct {
	// Every is how often health is checked (default 1m).
	Every string `json:"every,omitempty"`
	// DownAfter is how long health must fail, or heartbeats be missing,
	// before a box counts as down (default 5m).
	DownAfter string `json:"downAfter,omitempty"`
	// Listen is where boxes' heartbeats arrive (e.g. ":8099"); empty when
	// no box sends heartbeats here.
	Listen string `json:"listen,omitempty"`
	// Webhook gets a JSON POST per alert, with a text field chat tools show.
	Webhook string `json:"webhook,omitempty"`
	// Command runs per alert with sh -c: the alert text on stdin, and
	// TIFFIN_WATCH_BOX, TIFFIN_WATCH_STATE (down or up) and TIFFIN_WATCH_TEXT
	// in its environment. E.g. mail -s "tiffin watch" you@example.com
	Command string `json:"command,omitempty"`
	Boxes   []Box  `json:"boxes"`

	every, downAfter time.Duration
}

// Box is one box to watch.
type Box struct {
	Name string `json:"name"`
	// URL is the box's dashboard URL; its /v1/health is checked. Optional.
	URL string `json:"url,omitempty"`
	// Heartbeat is the secret path part the box pings: its monitor URL is
	// <collector>/ping/<heartbeat>. Optional; at least 16 characters.
	Heartbeat string `json:"heartbeat,omitempty"`
}

// Load reads and checks a watch file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, c.validate()
}

func (c *Config) validate() error {
	var err error
	if c.every, err = dur(c.Every, time.Minute); err != nil {
		return fmt.Errorf("every: %w", err)
	}
	if c.downAfter, err = dur(c.DownAfter, 5*time.Minute); err != nil {
		return fmt.Errorf("downAfter: %w", err)
	}
	if len(c.Boxes) == 0 {
		return errors.New("boxes: list at least one box")
	}
	if c.Webhook == "" && c.Command == "" {
		return errors.New("set webhook or command, or nobody hears about a box going down")
	}
	names, beats := map[string]bool{}, false
	for _, b := range c.Boxes {
		switch {
		case b.Name == "" || names[b.Name]:
			return fmt.Errorf("box %q: every box needs its own name", b.Name)
		case b.URL == "" && b.Heartbeat == "":
			return fmt.Errorf("box %s: set url, heartbeat or both", b.Name)
		case b.URL != "" && !strings.HasPrefix(b.URL, "https://") && !strings.HasPrefix(b.URL, "http://"):
			return fmt.Errorf("box %s: url must be http(s)", b.Name)
		case b.Heartbeat != "" && (len(b.Heartbeat) < 16 || strings.ContainsAny(b.Heartbeat, "/?#")):
			return fmt.Errorf("box %s: heartbeat must be a random string of 16+ characters (no / ? #), e.g. from openssl rand -hex 16", b.Name)
		}
		names[b.Name] = true
		beats = beats || b.Heartbeat != ""
	}
	if beats && c.Listen == "" {
		return errors.New("listen: boxes send heartbeats, so set where they arrive, e.g. \":8099\"")
	}
	return nil
}

func dur(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err == nil && d < 10*time.Second {
		err = errors.New("at least 10s")
	}
	return d, err
}

// Alert is one notification.
type Alert struct {
	Box     string   `json:"box"`
	State   string   `json:"state"` // down or up
	Text    string   `json:"text"`
	Reasons []string `json:"reasons,omitempty"`
}

type boxState struct {
	healthyAt time.Time // last good /v1/health (or the watch's start)
	healthErr string
	beatAt    time.Time // last heartbeat (or the watch's start)
	failing   string    // what the last /fail said; "" after a good ping
	down      bool
	downAt    time.Time
}

// Watcher checks boxes and alerts.
type Watcher struct {
	cfg    *Config
	client *http.Client
	now    func() time.Time
	notify func(context.Context, Alert) error
	log    io.Writer

	mu    sync.Mutex
	state map[string]*boxState
}

// New returns a watcher for cfg (from Load). Alerts go to cfg's webhook and
// command; log lines to log.
func New(cfg *Config, log io.Writer) *Watcher {
	w := &Watcher{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}, now: time.Now, log: log, state: map[string]*boxState{}}
	w.notify = w.deliver
	start := w.now()
	for _, b := range cfg.Boxes {
		w.state[b.Name] = &boxState{healthyAt: start, beatAt: start}
	}
	return w
}

// Notify replaces where alerts go (the webhook and command).
func (w *Watcher) Notify(f func(context.Context, Alert) error) { w.notify = f }

// Run checks every cfg.every and collects heartbeats until ctx ends.
func (w *Watcher) Run(ctx context.Context) error {
	errc := make(chan error, 1)
	if w.cfg.Listen != "" {
		srv := &http.Server{Addr: w.cfg.Listen, Handler: w.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() { errc <- srv.ListenAndServe() }()
		defer srv.Close()
		fmt.Fprintf(w.log, "collecting heartbeats on %s/ping/<heartbeat>\n", w.cfg.Listen)
	}
	t := time.NewTicker(w.cfg.every)
	defer t.Stop()
	for {
		w.Check(ctx)
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			return err
		case <-t.C:
		}
	}
}

// Check runs one round: health of every box with a URL, then alerts.
func (w *Watcher) Check(ctx context.Context) {
	for _, b := range w.cfg.Boxes {
		if b.URL == "" {
			continue
		}
		err := w.health(ctx, b.URL)
		w.mu.Lock()
		st := w.state[b.Name]
		if err == nil {
			st.healthyAt, st.healthErr = w.now(), ""
		} else {
			st.healthErr = err.Error()
		}
		w.mu.Unlock()
	}
	w.evaluate(ctx)
}

func (w *Watcher) health(ctx context.Context, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/health", nil)
	if err != nil {
		return err
	}
	res, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("/v1/health answered %s", res.Status)
	}
	return nil
}

// evaluate decides each box's state and alerts on every change.
func (w *Watcher) evaluate(ctx context.Context) {
	var alerts []Alert
	w.mu.Lock()
	now := w.now()
	for _, b := range w.cfg.Boxes {
		st := w.state[b.Name]
		reasons := w.reasons(b, st, now)
		switch down := len(reasons) > 0; {
		case down && !st.down:
			st.down, st.downAt = true, now
			alerts = append(alerts, Alert{Box: b.Name, State: "down", Reasons: reasons,
				Text: fmt.Sprintf("Tiffin box %s is down: %s.", b.Name, strings.Join(reasons, "; "))})
		case !down && st.down:
			st.down = false
			alerts = append(alerts, Alert{Box: b.Name, State: "up",
				Text: fmt.Sprintf("Tiffin box %s is back up after %s.", b.Name, now.Sub(st.downAt).Round(time.Second))})
		}
	}
	w.mu.Unlock()
	for _, a := range alerts {
		fmt.Fprintln(w.log, a.Text)
		if err := w.notify(ctx, a); err != nil {
			fmt.Fprintln(w.log, "alert not delivered:", err)
		}
	}
}

func (w *Watcher) reasons(b Box, st *boxState, now time.Time) []string {
	var out []string
	if b.URL != "" && now.Sub(st.healthyAt) >= w.cfg.downAfter {
		out = append(out, fmt.Sprintf("/v1/health has failed for %s (%s)", now.Sub(st.healthyAt).Round(time.Second), st.healthErr))
	}
	if b.Heartbeat != "" && now.Sub(st.beatAt) >= w.cfg.downAfter {
		out = append(out, fmt.Sprintf("no heartbeat for %s", now.Sub(st.beatAt).Round(time.Second)))
	}
	if st.failing != "" {
		out = append(out, "it reports failing checks: "+st.failing)
	}
	return out
}

// Handler collects heartbeats: GET or POST /ping/<heartbeat>, with /fail
// or /start after it (the healthchecks.io convention the box follows).
func (w *Watcher) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(rw, "GET or POST", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/ping/")
		token, kind, _ := strings.Cut(rest, "/")
		if kind != "" && kind != "fail" && kind != "start" {
			http.NotFound(rw, r)
			return
		}
		name := w.boxFor(token)
		if name == "" {
			http.NotFound(rw, r)
			return
		}
		var pl struct {
			Failing []string `json:"failing"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&pl)
		w.mu.Lock()
		st := w.state[name]
		st.beatAt, st.failing = w.now(), ""
		if kind == "fail" {
			st.failing = "unknown"
			if len(pl.Failing) > 0 {
				sort.Strings(pl.Failing)
				st.failing = strings.Join(pl.Failing, ", ")
			}
		}
		w.mu.Unlock()
		if kind == "fail" {
			// Alert now, after answering: the box waits for its ping.
			go w.evaluate(context.WithoutCancel(r.Context()))
		}
		_, _ = io.WriteString(rw, "OK\n")
	})
	return mux
}

func (w *Watcher) boxFor(token string) string {
	found := ""
	for _, b := range w.cfg.Boxes {
		if b.Heartbeat != "" && subtle.ConstantTimeCompare([]byte(b.Heartbeat), []byte(token)) == 1 {
			found = b.Name
		}
	}
	return found
}

// deliver sends an alert to the webhook and runs the command.
func (w *Watcher) deliver(ctx context.Context, a Alert) error {
	var errs []error
	if w.cfg.Webhook != "" {
		body, _ := json.Marshal(a)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.Webhook, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			var res *http.Response
			if res, err = w.client.Do(req); err == nil {
				res.Body.Close()
				if res.StatusCode/100 != 2 {
					err = fmt.Errorf("webhook answered %s", res.Status)
				}
			}
		}
		errs = append(errs, err)
	}
	if w.cfg.Command != "" {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "sh", "-c", w.cfg.Command)
		cmd.Stdin = strings.NewReader(a.Text + "\n")
		cmd.Env = append(os.Environ(), "TIFFIN_WATCH_BOX="+a.Box, "TIFFIN_WATCH_STATE="+a.State, "TIFFIN_WATCH_TEXT="+a.Text)
		if out, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("command: %w: %s", err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(errs...)
}
