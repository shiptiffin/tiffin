package protect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// CrowdSec runs as its own systemd service from its signed apt repository.
// Its local API listens on loopback only, on a port that clashes with
// nothing else on the box (the default 8080 is a local box's HTTP port).
// No console enrollment and no central API: nothing leaves the box.
const (
	lapiAddr       = "127.0.0.1:7422"
	bouncerName    = "tiffin-edge"
	bouncerKeyFile = "/var/lib/tiffin/protect/crowdsec-bouncer.key"
	accessLog      = "/var/lib/tiffin/logs/access.log"
	cscli          = "/usr/bin/cscli"
	crowdsecRepo   = "https://packagecloud.io/crowdsec/crowdsec/ubuntu/"
	crowdsecKey    = "https://packagecloud.io/crowdsec/crowdsec/gpgkey"
	// crowdsecVersion is the pinned package (apt verifies it with the repo's signing key).
	crowdsecVersion = "1.8.1"
)

// Hub items installed from the CrowdSec hub: the base pipeline (linux:
// raw/date/geoip parsers), Caddy's JSON access log, the generic HTTP and
// CVE scenarios, and the private-range whitelist that keeps the owner of a
// local box (whose traffic is all loopback) from ever being banned.
var hubItems = map[string][]string{
	"collections": {"crowdsecurity/linux", "crowdsecurity/caddy", "crowdsecurity/base-http-scenarios", "crowdsecurity/http-cve"},
	"parsers":     {"crowdsecurity/whitelists"},
}

// retry runs fn up to three times (the hub is downloaded over the network).
func retry(ctx context.Context, fn func() error) error {
	var err error
	for i := range 3 {
		if err = fn(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(i+1) * 3 * time.Second):
		}
	}
	return err
}

// crowdsecLocal overrides config.yaml: loopback LAPI on lapiAddr, no
// central API (no signals sent, no community blocklist pulled).
const crowdsecLocal = `# Managed by tiffin provision.
api:
  server:
    listen_uri: ` + lapiAddr + `
    online_client:
      credentials_path: ""
prometheus:
  listen_addr: 127.0.0.1
  listen_port: 7423
`

const acquisYAML = `# Managed by tiffin provision: the edge's JSON access log.
filenames:
  - ` + accessLog + `
labels:
  type: caddy
`

// Our scenarios. The hub's http scenarios cover scanners, probing and known
// CVEs; these add sign-in brute force and clients that keep ignoring 429s.
const authBFScenario = `# Managed by tiffin provision.
type: leaky
name: tiffin/http-auth-bruteforce
description: "Many failed or rate-limited sign-in attempts from one IP"
filter: |
  evt.Meta.log_type == 'http_access-log' &&
  evt.Parsed.verb in ['POST', 'PUT', 'PATCH'] &&
  evt.Meta.http_status in ['401', '403', '429'] &&
  evt.Meta.http_path matches '^/(api/auth/|auth/|log-?in|sign-?in|sign-?up|register|password|forgot-password|reset-password|users/sign_in|users/password|accounts/login|wp-login\\.php|xmlrpc\\.php)'
groupby: evt.Meta.source_ip
capacity: 10
leakspeed: 10s
blackhole: 1m
labels:
  service: http
  type: bruteforce
  remediation: true
  confidence: 3
  spoofable: 0
  classification:
    - attack.T1110
  behavior: "http:bruteforce"
  label: "Sign-in brute force"
`

const ratelimitScenario = `# Managed by tiffin provision.
type: leaky
name: tiffin/http-rate-limit-ignored
description: "An IP keeps hammering the box after being rate limited"
filter: "evt.Meta.log_type == 'http_access-log' && evt.Meta.http_status == '429'"
groupby: evt.Meta.source_ip
capacity: 100
leakspeed: 1s
blackhole: 2m
labels:
  service: http
  type: flood
  remediation: true
  confidence: 3
  spoofable: 0
  behavior: "http:dos"
  label: "Ignores rate limits"
`

func provisionCrowdSec(ctx context.Context, s *platform.System) error {
	// The .local override must exist before the package's postinst starts
	// the service, so the API never tries to bind 8080.
	changed := false
	for path, body := range map[string]string{
		"/etc/crowdsec/config.yaml.local":                          crowdsecLocal,
		"/etc/crowdsec/acquis.d/tiffin.yaml":                       acquisYAML,
		"/etc/crowdsec/scenarios/tiffin-http-auth-bruteforce.yaml": authBFScenario,
		"/etc/crowdsec/scenarios/tiffin-http-rate-limit.yaml":      ratelimitScenario,
	} {
		c, err := s.WriteFile(path, []byte(body), 0o644)
		if err != nil {
			return err
		}
		changed = changed || c
	}
	// CrowdSec only tails files that exist when it starts; the edge creates
	// the access log on first start, which on a new box comes later.
	if _, err := os.Stat(accessLog); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(accessLog), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(accessLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		f.Close()
		changed = true
	}
	if err := s.AptRepo(ctx, "crowdsec", crowdsecKey, "deb [signed-by={key}] "+crowdsecRepo+" "+platform.RepoCodename("jammy", "noble")+" main"); err != nil {
		return err
	}
	if err := s.Apt(ctx, "crowdsec="+crowdsecVersion); err != nil {
		return err
	}
	// Install what detection needs explicitly: the package's own service
	// detection is best effort and skipped when the hub is slow to answer.
	for _, kind := range []string{"collections", "parsers"} {
		out, err := s.Run(ctx, cscli, kind, "list", "-o", "json")
		if err != nil {
			return err
		}
		var missing []string
		for _, c := range hubItems[kind] {
			if !strings.Contains(out, `"`+c+`"`) {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			continue
		}
		s.Log("installing CrowdSec " + kind + " " + strings.Join(missing, ", "))
		if err := retry(ctx, func() error {
			if _, err := s.Run(ctx, cscli, "hub", "update"); err != nil {
				return err
			}
			_, err := s.Run(ctx, cscli, append([]string{kind, "install"}, missing...)...)
			return err
		}); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		s.Log("restarting crowdsec")
		if _, err := s.Run(ctx, "systemctl", "restart", "crowdsec"); err != nil {
			return err
		}
	} else if _, err := s.Run(ctx, "systemctl", "is-active", "--quiet", "crowdsec"); err != nil {
		if _, err := s.Run(ctx, "systemctl", "restart", "crowdsec"); err != nil {
			return err
		}
	}
	if err := s.WaitTCP(ctx, lapiAddr, 60*time.Second); err != nil {
		return fmt.Errorf("crowdsec API did not come up on %s: %w", lapiAddr, err)
	}
	return ensureBouncer(ctx, s)
}

// ensureBouncer makes sure the edge has a working bouncer key on disk.
func ensureBouncer(ctx context.Context, s *platform.System) error {
	out, err := s.Run(ctx, cscli, "bouncers", "list", "-o", "json")
	if err != nil {
		return err
	}
	key, _ := os.ReadFile(bouncerKeyFile)
	if strings.Contains(out, `"`+bouncerName+`"`) && len(strings.TrimSpace(string(key))) >= 32 {
		return nil
	}
	s.Log("creating the edge's CrowdSec bouncer key")
	_, _ = s.Run(ctx, cscli, "bouncers", "delete", bouncerName)
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	k := hex.EncodeToString(b)
	if _, err := s.Run(ctx, cscli, "bouncers", "add", bouncerName, "-k", k); err != nil {
		return err
	}
	if err := os.MkdirAll("/var/lib/tiffin/protect", 0o700); err != nil {
		return err
	}
	_, err = s.WriteFile(bouncerKeyFile, []byte(k+"\n"), 0o600)
	return err
}

// ---- runtime: cscli ----

// Decision is one active CrowdSec decision.
type Decision struct {
	ID       int64     `json:"id"`
	Value    string    `json:"value" doc:"The IP or range."`
	Scope    string    `json:"scope" doc:"Ip or Range."`
	Type     string    `json:"type" doc:"ban (the edge answers 403)."`
	Origin   string    `json:"origin" doc:"crowdsec (a scenario fired) or cscli (a manual ban)."`
	Scenario string    `json:"scenario" doc:"Why: the scenario name or the ban reason."`
	Until    time.Time `json:"until,omitzero"`
	Expires  string    `json:"expiresIn" doc:"Time left, e.g. 3h59m."`
	Country  string    `json:"country,omitempty"`
	AS       string    `json:"as,omitempty" doc:"The network's owner, when known."`
}

// Alert is one thing CrowdSec noticed.
type Alert struct {
	ID        int64     `json:"id"`
	Scenario  string    `json:"scenario"`
	Message   string    `json:"message"`
	Source    string    `json:"source" doc:"The IP or range."`
	Events    int       `json:"events"`
	Decisions int       `json:"decisions" doc:"Decisions (bans) it produced."`
	At        time.Time `json:"at"`
}

type crowdsec interface {
	Installed() bool
	Pipeline() error // nil when the parsers detection needs are in place
	Running(ctx context.Context) bool
	Decisions(ctx context.Context) ([]Decision, error)
	Alerts(ctx context.Context, limit int) ([]Alert, error)
	Ban(ctx context.Context, target string, d time.Duration, reason string) error
	Unban(ctx context.Context, target string) (int, error)
}

// ErrNotInstalled means CrowdSec is not on this machine.
var ErrNotInstalled = errors.New("CrowdSec is not installed on this machine")

type cscliCrowdSec struct{}

func (cscliCrowdSec) Installed() bool { _, err := os.Stat(cscli); return err == nil }

// pipelineFiles are the hub files detection on the edge's log depends on.
var pipelineFiles = []string{
	"/etc/crowdsec/parsers/s00-raw/syslog-logs.yaml",
	"/etc/crowdsec/parsers/s01-parse/caddy-logs.yaml",
	"/etc/crowdsec/parsers/s02-enrich/http-logs.yaml",
	"/etc/crowdsec/parsers/s02-enrich/whitelists.yaml",
	"/etc/crowdsec/acquis.d/tiffin.yaml",
}

func (cscliCrowdSec) Pipeline() error {
	var missing []string
	for _, f := range pipelineFiles {
		if _, err := os.Stat(f); err != nil {
			missing = append(missing, filepath.Base(f))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s; run `sudo tiffin provision`", strings.Join(missing, ", "))
	}
	return nil
}

func (cscliCrowdSec) Running(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "crowdsec").Run() == nil
}

func run(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := os.Stat(cscli); err != nil {
		return nil, ErrNotInstalled
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cscli, args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("cscli %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

type cscliDecision struct {
	ID       int64  `json:"id"`
	Value    string `json:"value"`
	Scope    string `json:"scope"`
	Type     string `json:"type"`
	Origin   string `json:"origin"`
	Scenario string `json:"scenario"`
	Duration string `json:"duration"`
	Until    string `json:"until"`
}

type cscliAlert struct {
	ID          int64           `json:"id"`
	Scenario    string          `json:"scenario"`
	Message     string          `json:"message"`
	EventsCount int             `json:"events_count"`
	CreatedAt   string          `json:"created_at"`
	StartAt     string          `json:"start_at"`
	Decisions   []cscliDecision `json:"decisions"`
	Source      struct {
		Value  string `json:"value"`
		IP     string `json:"ip"`
		Range  string `json:"range"`
		Scope  string `json:"scope"`
		CN     string `json:"cn"`
		ASName string `json:"as_name"`
	} `json:"source"`
}

func parseAlerts(raw []byte) ([]cscliAlert, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var out []cscliAlert
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse cscli output: %w", err)
	}
	return out, nil
}

func decisionsFrom(alerts []cscliAlert) []Decision {
	out := []Decision{}
	for _, a := range alerts {
		for _, d := range a.Decisions {
			dec := Decision{ID: d.ID, Value: d.Value, Scope: d.Scope, Type: d.Type, Origin: d.Origin, Scenario: d.Scenario,
				Expires: d.Duration, Country: a.Source.CN, AS: a.Source.ASName}
			if t, err := time.Parse(time.RFC3339, d.Until); err == nil {
				dec.Until = t
			}
			if strings.HasPrefix(dec.Expires, "-") {
				continue // already expired
			}
			out = append(out, dec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func alertsFrom(alerts []cscliAlert) []Alert {
	out := []Alert{}
	for _, a := range alerts {
		src := a.Source.Value
		if src == "" {
			src = a.Source.IP
		}
		al := Alert{ID: a.ID, Scenario: a.Scenario, Message: a.Message, Source: src, Events: a.EventsCount}
		for _, d := range a.Decisions {
			if !strings.HasPrefix(d.Duration, "-") {
				al.Decisions++
			}
		}
		for _, s := range []string{a.CreatedAt, a.StartAt} {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				al.At = t
				break
			}
		}
		out = append(out, al)
	}
	return out
}

func (cscliCrowdSec) Decisions(ctx context.Context) ([]Decision, error) {
	raw, err := run(ctx, "decisions", "list", "-o", "json")
	if err != nil {
		return nil, err
	}
	alerts, err := parseAlerts(raw)
	return decisionsFrom(alerts), err
}

func (cscliCrowdSec) Alerts(ctx context.Context, limit int) ([]Alert, error) {
	raw, err := run(ctx, "alerts", "list", "-o", "json", "--limit", strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	alerts, err := parseAlerts(raw)
	return alertsFrom(alerts), err
}

// targetArgs returns cscli's --ip or --range for an IP or CIDR.
func targetArgs(target string) ([]string, error) {
	if a, err := netip.ParseAddr(target); err == nil {
		return []string{"--ip", a.Unmap().String()}, nil
	}
	if p, err := netip.ParsePrefix(target); err == nil {
		return []string{"--range", p.Masked().String()}, nil
	}
	return nil, fmt.Errorf("%q is not an IP address or range", target)
}

func (cscliCrowdSec) Ban(ctx context.Context, target string, d time.Duration, reason string) error {
	t, err := targetArgs(target)
	if err != nil {
		return err
	}
	_, err = run(ctx, append(append([]string{"decisions", "add"}, t...), "--duration", d.String(), "--type", "ban", "--reason", reason)...)
	return err
}

var deletedRE = regexp.MustCompile(`(\d+) decision\(s\) deleted`)

func (cscliCrowdSec) Unban(ctx context.Context, target string) (int, error) {
	t, err := targetArgs(target)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cscli, append([]string{"decisions", "delete"}, t...)...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("cscli decisions delete: %s", strings.TrimSpace(string(out)))
	}
	if m := deletedRE.FindSubmatch(out); m != nil {
		n, _ := strconv.Atoi(string(m[1]))
		return n, nil
	}
	return 0, nil
}
