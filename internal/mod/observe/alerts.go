package observe

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/mod/backup"
	"github.com/btahir/tiffin/internal/mod/email"
	"github.com/btahir/tiffin/internal/mod/email/templates"
	"github.com/btahir/tiffin/internal/platform"
)

// Rule kinds.
const (
	KindDisk         = "disk"          // data or root disk used, percent
	KindMemory       = "memory"        // memory used, percent
	KindCertExpiry   = "cert_expiry"   // hours until an edge certificate expires
	KindBackupAge    = "backup_age"    // hours since the newest file in /var/lib/tiffin/backups
	KindOffsiteAge   = "offsite_age"   // hours since the newest copy of the backups off the box
	KindDrillFailed  = "drill_failed"  // 1 when the last restore drill failed
	KindErrorSpike   = "error_spike"   // error events (Sentry protocol) per project in the last 5 minutes
	KindUnitRestarts = "unit_restarts" // restarts of a box service in the last 15 minutes
	KindUnitDown     = "unit_down"     // a box service is not active
	KindPromQL       = "promql"        // any PromQL expression; every series above the threshold fires
)

// RuleKinds lists every rule kind.
var RuleKinds = []string{KindDisk, KindMemory, KindCertExpiry, KindBackupAge, KindOffsiteAge, KindDrillFailed, KindErrorSpike, KindUnitRestarts, KindUnitDown, KindPromQL}

// Rule is one alert rule.
type Rule struct {
	Name        string  `json:"name" doc:"Rule name (lowercase slug)"`
	Kind        string  `json:"kind" enum:"disk,memory,cert_expiry,backup_age,offsite_age,drill_failed,error_spike,unit_restarts,unit_down,promql" doc:"What to watch. disk/memory: percent used. cert_expiry: hours left (short-lived internal certificates fire when past 80% of their lifetime). backup_age: hours since the newest backup file. offsite_age: hours since the newest copy of the backups off the box (silent while copies are off). drill_failed: 1 when the last restore drill failed. error_spike: error events per project in 5 minutes. unit_restarts: restarts of a box service in 15 minutes. unit_down: a box service is not running. promql: any expression, fires per series above the threshold."`
	Threshold   float64 `json:"threshold" doc:"Fires when the value is above this (below, for cert_expiry)"`
	ForSeconds  int     `json:"forSeconds,omitempty" minimum:"0" maximum:"86400" doc:"The condition must hold this long before the alert fires. Default 0."`
	Project     string  `json:"project,omitempty" doc:"error_spike only: watch one project (default every project)"`
	Expr        string  `json:"expr,omitempty" maxLength:"2000" doc:"promql only: the expression"`
	Enabled     bool    `json:"enabled"`
	Description string  `json:"description,omitempty" maxLength:"300"`
}

// DefaultRules are installed on first start and can be edited or disabled.
var DefaultRules = []Rule{
	{Name: "disk-full", Kind: KindDisk, Threshold: 85, Enabled: true, Description: "A disk is more than 85% full"},
	{Name: "memory-high", Kind: KindMemory, Threshold: 90, ForSeconds: 300, Enabled: true, Description: "Memory has been more than 90% used for 5 minutes"},
	{Name: "cert-expiring", Kind: KindCertExpiry, Threshold: 72, Enabled: true, Description: "An HTTPS certificate expires within 72 hours and has not renewed"},
	{Name: "backup-stale", Kind: KindBackupAge, Threshold: 26, Enabled: true, Description: "The newest backup is more than 26 hours old (silent until the first backup exists)"},
	{Name: "offsite-stale", Kind: KindOffsiteAge, Threshold: 26, Enabled: true, Description: "The newest copy of the backups off the box is more than 26 hours old (silent while copies are off)"},
	{Name: "restore-drill-failed", Kind: KindDrillFailed, Threshold: 0, Enabled: true, Description: "The last restore drill, of the local or the off-box copy, failed"},
	{Name: "error-spike", Kind: KindErrorSpike, Threshold: 20, Enabled: true, Description: "More than 20 errors reported by a project's apps in 5 minutes"},
	{Name: "service-restarts", Kind: KindUnitRestarts, Threshold: 3, Enabled: true, Description: "A box service restarted more than 3 times in 15 minutes"},
	{Name: "service-down", Kind: KindUnitDown, Threshold: 0, ForSeconds: 60, Enabled: true, Description: "A box service has not been running for a minute"},
}

var ruleName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Validate checks a rule.
func (r *Rule) Validate() error {
	if !ruleName.MatchString(r.Name) {
		return errors.New("rule name must be a lowercase slug (letters, digits, dashes)")
	}
	ok := false
	for _, k := range RuleKinds {
		ok = ok || k == r.Kind
	}
	if !ok {
		return fmt.Errorf("unknown kind %q; one of %s", r.Kind, strings.Join(RuleKinds, ", "))
	}
	if r.Kind == KindPromQL && strings.TrimSpace(r.Expr) == "" {
		return errors.New("a promql rule needs expr")
	}
	if r.Kind != KindPromQL && r.Expr != "" {
		return errors.New("expr is only for promql rules")
	}
	if r.Project != "" && r.Kind != KindErrorSpike {
		return errors.New("project is only for error_spike rules")
	}
	if math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) {
		return errors.New("threshold must be a number")
	}
	return nil
}

// Rules lists rules by name.
func (s *Store) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var r Rule
		if json.Unmarshal([]byte(b), &r) == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// PutRule creates or replaces a rule.
func (s *Store) PutRule(ctx context.Context, r Rule) error { return s.ReplaceRule(ctx, r, nil) }

// ReplaceRule creates or replaces a rule once allow (when set) approves the
// rule it replaces (nil when there is none). The check and the write happen
// under the writer lock, so the rule allow saw is the rule replaced.
func (s *Store) ReplaceRule(ctx context.Context, r Rule, allow func(old *Rule) error) error {
	b, _ := json.Marshal(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if allow != nil {
		var old *Rule
		var body string
		err := s.db.QueryRowContext(ctx, `SELECT body FROM alert_rules WHERE name = ?`, r.Name).Scan(&body)
		switch {
		case err == nil:
			old = &Rule{}
			if err := json.Unmarshal([]byte(body), old); err != nil {
				return err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if err := allow(old); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_rules(name, body) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET body = excluded.body`, r.Name, string(b))
	return err
}

// DeleteRule removes a rule and its live alerts. It reports whether it existed.
func (s *Store) DeleteRule(ctx context.Context, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM alert_state WHERE rule = ?`, name)
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// laterRules are default rules added after boxes were first seeded: each is
// installed once on those boxes too.
var laterRules = []string{"offsite-stale", "restore-drill-failed"}

// EnsureDefaultRules installs the default rules once (and each of
// laterRules once on boxes seeded before it existed).
func (s *Store) EnsureDefaultRules(ctx context.Context) error {
	first := s.Setting(ctx, "rules.seeded") == ""
	for _, r := range DefaultRules {
		later := slices.Contains(laterRules, r.Name)
		if !first && (!later || s.Setting(ctx, "rules.seeded."+r.Name) != "") {
			continue
		}
		if err := s.PutRule(ctx, r); err != nil {
			return err
		}
		if later {
			if err := s.SetSetting(ctx, "rules.seeded."+r.Name, now()); err != nil {
				return err
			}
		}
	}
	if !first {
		return nil
	}
	return s.SetSetting(ctx, "rules.seeded", now())
}

// Alert is a rule's live state for one subject (a disk, a unit, a project).
type Alert struct {
	Rule    string    `json:"rule"`
	Subject string    `json:"subject" doc:"What the alert is about: a mount, a service, a project, a certificate host"`
	Project string    `json:"project,omitempty" doc:"The project, for project alerts (error_spike)"`
	State   string    `json:"state" enum:"firing,ok"`
	Value   float64   `json:"value"`
	Since   time.Time `json:"since"`
	Summary string    `json:"summary"`
}

// HistoryEntry is one alert transition and how it was delivered.
type HistoryEntry struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Rule     string    `json:"rule"`
	Subject  string    `json:"subject"`
	State    string    `json:"state" enum:"firing,resolved,test"`
	Value    float64   `json:"value"`
	Summary  string    `json:"summary"`
	Delivery string    `json:"delivery" doc:"Where the notification went, or why it did not"`
}

// Firing returns alerts currently firing.
func (s *Store) Firing(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rule, subject, value, since, summary FROM alert_state WHERE firing = 1 ORDER BY since DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		var since string
		if err := rows.Scan(&a.Rule, &a.Subject, &a.Value, &since, &a.Summary); err != nil {
			return nil, err
		}
		a.State, a.Since = "firing", parseT(since)
		a.Project = projectOfSubject(a.Subject)
		out = append(out, a)
	}
	return out, rows.Err()
}

// History returns recent transitions, newest first.
func (s *Store) History(ctx context.Context, limit int) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, rule, subject, state, value, summary, delivery FROM alert_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		var at string
		if err := rows.Scan(&h.ID, &at, &h.Rule, &h.Subject, &h.State, &h.Value, &h.Summary, &h.Delivery); err != nil {
			return nil, err
		}
		h.At = parseT(at)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) addHistory(ctx context.Context, h HistoryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.ExecContext(ctx, `INSERT INTO alert_history(at, rule, subject, state, value, summary, delivery) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		h.At.UTC().Format(time.RFC3339Nano), h.Rule, h.Subject, h.State, h.Value, h.Summary, h.Delivery)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM alert_history WHERE id <= (SELECT MAX(id) - 1000 FROM alert_history)`)
}

// projectOfSubject: error_spike subjects are "project:<name>".
func projectOfSubject(subject string) string {
	p, _ := strings.CutPrefix(subject, "project:")
	if p == subject {
		return ""
	}
	return p
}

// finding is one evaluated condition.
type finding struct {
	Subject string
	Value   float64
	Bad     bool
	Summary string
}

// Alerter evaluates rules and delivers transitions.
type Alerter struct {
	Store     *Store
	Collector *Collector
	Victoria  *Victoria
	Platform  *platform.Platform
	CertDir   string // Caddy storage, e.g. /var/lib/tiffin/platform/edge
	BackupDir string
	Box       string // box name for notifications (its domain)

	evalMu   sync.Mutex // one evaluation at a time, so a transition is notified once
	mu       sync.Mutex
	pending  map[string]time.Time // rule\x00subject -> when the condition started
	restarts map[string][]restartSample
}

type restartSample struct {
	at time.Time
	n  int
}

// Settings keys.
const (
	SettingWebhook = "alerts.webhook"
	SettingEmail   = "alerts.email"
	// SettingEmailProject names the project whose email service sends box
	// alerts (mail lands in its dev inbox until an SMTP relay is set up).
	SettingEmailProject = "alerts.emailProject"
)

// Evaluate runs every enabled rule once and records transitions.
func (a *Alerter) Evaluate(ctx context.Context) error {
	a.evalMu.Lock()
	defer a.evalMu.Unlock()
	rules, err := a.Store.Rules(ctx)
	if err != nil {
		return err
	}
	snap := a.Collector.Latest()
	a.trackRestarts(snap)
	seen := map[string]bool{}
	settled := map[string]bool{} // rules whose missing subjects may resolve
	for _, r := range rules {
		if !r.Enabled {
			settled[r.Name] = true
			continue
		}
		fs, err := a.eval(ctx, r, snap)
		if err != nil {
			continue // a rule that cannot be evaluated now keeps its state
		}
		settled[r.Name] = true
		for _, f := range fs {
			seen[r.Name+"\x00"+f.Subject] = true
			a.transition(ctx, r, f)
		}
	}
	// Subjects that disappeared (a deleted unit, a disabled rule) resolve.
	rows, err := a.Store.db.QueryContext(ctx, `SELECT rule, subject, value FROM alert_state WHERE firing = 1`)
	if err != nil {
		return err
	}
	var gone []finding
	var goneRules []string
	for rows.Next() {
		var rule, subj string
		var v float64
		if rows.Scan(&rule, &subj, &v) == nil && settled[rule] && !seen[rule+"\x00"+subj] {
			gone = append(gone, finding{Subject: subj, Value: v, Summary: "no longer applies"})
			goneRules = append(goneRules, rule)
		}
	}
	rows.Close()
	for i, f := range gone {
		r := Rule{Name: goneRules[i]}
		for _, x := range rules {
			if x.Name == r.Name {
				r = x
			}
		}
		r.Enabled = false // resolves
		a.transition(ctx, r, f)
	}
	return nil
}

func (a *Alerter) trackRestarts(snap *Snapshot) {
	if snap == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.restarts == nil {
		a.restarts = map[string][]restartSample{}
	}
	cut := time.Now().Add(-16 * time.Minute)
	for _, u := range snap.Units {
		s := append(a.restarts[u.Name], restartSample{snap.At, u.Restarts})
		for len(s) > 1 && s[0].at.Before(cut) {
			s = s[1:]
		}
		a.restarts[u.Name] = s
	}
}

func pct(v float64) float64 { return math.Round(v*1000) / 10 }

func (a *Alerter) eval(ctx context.Context, r Rule, snap *Snapshot) ([]finding, error) {
	switch r.Kind {
	case KindDisk:
		if snap == nil {
			return nil, errors.New("no sample yet")
		}
		var out []finding
		for _, d := range snap.Disks {
			v := pct(d.UsedRatio)
			out = append(out, finding{Subject: d.Mount, Value: v, Bad: v > r.Threshold,
				Summary: fmt.Sprintf("disk %s is %.1f%% full (%s free of %s)", d.Mount, v, bytesH(d.Free), bytesH(d.Total))})
		}
		return out, nil
	case KindMemory:
		if snap == nil || snap.MemTotal == 0 {
			return nil, errors.New("no sample yet")
		}
		v := pct(snap.MemUsed)
		return []finding{{Subject: "memory", Value: v, Bad: v > r.Threshold,
			Summary: fmt.Sprintf("memory is %.1f%% used (%s available of %s)", v, bytesH(snap.MemAvail), bytesH(snap.MemTotal))}}, nil
	case KindCertExpiry:
		return a.certs(r)
	case KindBackupAge:
		return a.backups(ctx, r)
	case KindOffsiteAge:
		h, on, sum := backup.OffsiteAge(ctx, a.Platform)
		if !on {
			return nil, nil // copies off the box are off: the backups page says so
		}
		h = math.Round(h*10) / 10
		return []finding{{Subject: "offsite", Value: h, Bad: h > r.Threshold, Summary: fmt.Sprintf("the newest off-box copy is %.1f hours old: %s", h, sum)}}, nil
	case KindDrillFailed:
		failed, any, sum := backup.LastDrillFailed(ctx, a.Platform)
		if !any {
			return nil, nil
		}
		v := 0.0
		if failed {
			v = 1
		}
		return []finding{{Subject: "restore-drill", Value: v, Bad: v > r.Threshold, Summary: sum}}, nil
	case KindErrorSpike:
		counts, err := a.Store.ErrorCount(ctx, 5*time.Minute)
		if err != nil {
			return nil, err
		}
		var out []finding
		projects, _ := a.Platform.DB.ListProjects(ctx)
		for _, p := range projects {
			if r.Project != "" && p != r.Project {
				continue
			}
			n := float64(counts[p])
			out = append(out, finding{Subject: "project:" + p, Value: n, Bad: n > r.Threshold,
				Summary: fmt.Sprintf("project %s reported %d errors in the last 5 minutes", p, counts[p])})
		}
		return out, nil
	case KindUnitRestarts:
		a.mu.Lock()
		defer a.mu.Unlock()
		var out []finding
		for unit, s := range a.restarts {
			if len(s) == 0 {
				continue
			}
			n := float64(s[len(s)-1].n - s[0].n)
			if n < 0 {
				n = float64(s[len(s)-1].n)
			}
			out = append(out, finding{Subject: unit, Value: n, Bad: n > r.Threshold,
				Summary: fmt.Sprintf("service %s restarted %d times in 15 minutes", strings.TrimSuffix(unit, ".service"), int(n))})
		}
		return out, nil
	case KindUnitDown:
		if snap == nil {
			return nil, errors.New("no sample yet")
		}
		var out []finding
		for _, u := range snap.Units {
			bad := !u.Active && u.State != "inactive" // stopped on purpose (inactive) is not down
			v := 0.0
			if bad {
				v = 1
			}
			out = append(out, finding{Subject: u.Name, Value: v, Bad: bad,
				Summary: fmt.Sprintf("service %s is %s", strings.TrimSuffix(u.Name, ".service"), u.State)})
		}
		return out, nil
	case KindPromQL:
		res, err := a.Victoria.QueryMetrics(ctx, r.Expr, time.Time{}, time.Time{}, 0)
		if err != nil {
			return nil, err
		}
		var vec []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if res.ResultType == "scalar" {
			var sc [2]any
			if json.Unmarshal(res.Result, &sc) == nil {
				vec = append(vec, struct {
					Metric map[string]string `json:"metric"`
					Value  [2]any            `json:"value"`
				}{nil, sc})
			}
		} else if err := json.Unmarshal(res.Result, &vec); err != nil {
			return nil, err
		}
		var out []finding
		for _, v := range vec {
			var f float64
			fmt.Sscan(fmt.Sprint(v.Value[1]), &f)
			subj := labelString(v.Metric)
			out = append(out, finding{Subject: subj, Value: f, Bad: f > r.Threshold,
				Summary: fmt.Sprintf("%s = %g (threshold %g)", subj, f, r.Threshold)})
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown kind %q", r.Kind)
}

func labelString(m map[string]string) string {
	if len(m) == 0 {
		return "value"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", k, m[k])
	}
	b.WriteByte('}')
	return b.String()
}

func (a *Alerter) certs(r Rule) ([]finding, error) {
	root := filepath.Join(a.CertDir, "certificates")
	var out []finding
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".crt") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil
		}
		left := time.Until(c.NotAfter).Hours()
		life := c.NotAfter.Sub(c.NotBefore).Hours()
		limit := math.Min(r.Threshold, life*0.2)
		name := strings.TrimSuffix(filepath.Base(path), ".crt")
		out = append(out, finding{Subject: name, Value: math.Round(left*10) / 10, Bad: left < limit,
			Summary: fmt.Sprintf("certificate for %s expires in %.1f hours (%s)", name, left, c.NotAfter.UTC().Format(time.RFC3339))})
		return nil
	})
	return out, nil
}

// backups judges the newest successful backup (the backup module's record;
// files in the backup directory when the module has none).
func (a *Alerter) backups(ctx context.Context, r Rule) ([]finding, error) {
	var newest time.Time
	lastFailed := ""
	if a.Platform != nil && a.Platform.DB != nil {
		if bs, err := backup.List(ctx, a.Platform); err == nil {
			for i, b := range bs { // newest first
				if i == 0 && b.Status == "failed" {
					lastFailed = b.Error
				}
				if b.Status == "ok" && b.FinishedAt.After(newest) {
					newest = b.FinishedAt
				}
			}
		}
	}
	if newest.IsZero() {
		_ = filepath.WalkDir(a.BackupDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.Count(strings.TrimPrefix(path, a.BackupDir), "/") > 4 {
					return filepath.SkipDir
				}
				return nil
			}
			if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
			return nil
		})
	}
	if newest.IsZero() {
		return nil, nil // no backups yet: nothing to judge
	}
	h := math.Round(time.Since(newest).Hours()*10) / 10
	sum := fmt.Sprintf("the newest backup is %.1f hours old", h)
	if lastFailed != "" {
		sum += "; the latest attempt failed: " + lastFailed
	}
	return []finding{{Subject: "backups", Value: h, Bad: h > r.Threshold, Summary: sum}}, nil
}

func (a *Alerter) transition(ctx context.Context, r Rule, f finding) {
	key := r.Name + "\x00" + f.Subject
	var firing int
	var since string
	err := a.Store.db.QueryRowContext(ctx, `SELECT firing, since FROM alert_state WHERE rule = ? AND subject = ?`, r.Name, f.Subject).Scan(&firing, &since)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return
	}
	bad := f.Bad && r.Enabled
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[string]time.Time{}
	}
	if bad && firing == 0 {
		start, ok := a.pending[key]
		if !ok {
			start = time.Now()
			a.pending[key] = start
		}
		if time.Since(start) < time.Duration(r.ForSeconds)*time.Second {
			a.mu.Unlock()
			return // pending
		}
	}
	if !bad {
		delete(a.pending, key)
	}
	a.mu.Unlock()
	switch {
	case bad && firing == 0:
		a.Store.mu.Lock()
		_, _ = a.Store.db.ExecContext(ctx, `INSERT INTO alert_state(rule, subject, firing, value, since, summary) VALUES (?, ?, 1, ?, ?, ?)
			ON CONFLICT(rule, subject) DO UPDATE SET firing = 1, value = excluded.value, since = excluded.since, summary = excluded.summary`,
			r.Name, f.Subject, f.Value, now(), f.Summary)
		a.Store.mu.Unlock()
		a.notify(ctx, r, f, "firing")
	case bad:
		a.Store.mu.Lock()
		_, _ = a.Store.db.ExecContext(ctx, `UPDATE alert_state SET value = ?, summary = ? WHERE rule = ? AND subject = ?`, f.Value, f.Summary, r.Name, f.Subject)
		a.Store.mu.Unlock()
	case firing == 1:
		a.Store.mu.Lock()
		_, _ = a.Store.db.ExecContext(ctx, `UPDATE alert_state SET firing = 0, value = ?, since = ?, summary = ? WHERE rule = ? AND subject = ?`,
			f.Value, now(), f.Summary, r.Name, f.Subject)
		a.Store.mu.Unlock()
		a.notify(ctx, r, f, "resolved")
	case !known:
		// Remember healthy subjects too so the dashboard can list them.
		a.Store.mu.Lock()
		_, _ = a.Store.db.ExecContext(ctx, `INSERT INTO alert_state(rule, subject, firing, value, since, summary) VALUES (?, ?, 0, ?, ?, ?) ON CONFLICT DO NOTHING`,
			r.Name, f.Subject, f.Value, now(), f.Summary)
		a.Store.mu.Unlock()
	default:
		a.Store.mu.Lock()
		_, _ = a.Store.db.ExecContext(ctx, `UPDATE alert_state SET value = ?, summary = ? WHERE rule = ? AND subject = ?`, f.Value, f.Summary, r.Name, f.Subject)
		a.Store.mu.Unlock()
	}
}

// Notification is the webhook payload.
type Notification struct {
	Box         string    `json:"box"`
	Rule        string    `json:"rule"`
	Kind        string    `json:"kind"`
	Subject     string    `json:"subject"`
	Project     string    `json:"project,omitempty"`
	State       string    `json:"state"`
	Value       float64   `json:"value"`
	Threshold   float64   `json:"threshold"`
	Summary     string    `json:"summary"`
	Description string    `json:"description,omitempty"`
	At          time.Time `json:"at"`
	Dashboard   string    `json:"dashboard,omitempty"`
	Text        string    `json:"text" doc:"One line for chat webhooks (Slack, Discord and others read this field)"`
}

// emailProject picks the project whose email service carries an alert: the
// alert's own project when it has email, else the configured one.
func (a *Alerter) emailProject(ctx context.Context, alertProject string) string {
	has := func(project string) bool {
		if project == "" || a.Platform == nil {
			return false
		}
		_, res, err := a.Platform.DB.Load(ctx, project)
		if err != nil {
			return false
		}
		_, ok := res["service/email"]
		return ok
	}
	if has(alertProject) {
		return alertProject
	}
	if p := a.Store.Setting(ctx, SettingEmailProject); has(p) {
		return p
	}
	return ""
}

func (a *Alerter) notify(ctx context.Context, r Rule, f finding, state string) {
	n := Notification{Box: a.Box, Rule: r.Name, Kind: r.Kind, Subject: f.Subject, Project: projectOfSubject(f.Subject), State: state,
		Value: f.Value, Threshold: r.Threshold, Summary: f.Summary, Description: r.Description, At: time.Now().UTC()}
	if a.Platform != nil {
		n.Dashboard = a.Platform.PublicURL
	}
	icon := "FIRING"
	if state == "resolved" {
		icon = "RESOLVED"
	} else if state == "test" {
		icon = "TEST"
	}
	n.Text = fmt.Sprintf("[%s] %s on %s: %s", icon, r.Name, a.Box, f.Summary)
	delivery := a.deliver(ctx, n)
	a.Store.addHistory(ctx, HistoryEntry{At: n.At, Rule: r.Name, Subject: f.Subject, State: state, Value: f.Value, Summary: f.Summary, Delivery: delivery})
}

// deliver sends a notification to every configured channel and describes
// the outcome in plain words.
func (a *Alerter) deliver(ctx context.Context, n Notification) string {
	var parts []string
	if hook := a.Store.Setting(ctx, SettingWebhook); hook != "" {
		if err := postWebhook(ctx, hook, n); err != nil {
			parts = append(parts, "webhook failed: "+err.Error())
		} else {
			parts = append(parts, "webhook ok")
		}
	}
	to := a.Store.Setting(ctx, SettingEmail)
	if to == "" {
		to = "alerts@" + a.Box
	}
	if project := a.emailProject(ctx, n.Project); project != "" {
		msg := email.Message{To: []string{to}, Subject: n.Text, Text: notificationText(n)}
		if e, err := a.alertEmail(n); err == nil {
			msg.Subject, msg.Text, msg.HTML = e.Subject, e.Text, e.HTML
		}
		res, err := email.Send(ctx, a.Platform, project, msg)
		switch {
		case err != nil:
			parts = append(parts, "email failed: "+err.Error())
		case res.Delivery == "inbox":
			parts = append(parts, "email to "+to+" captured in project "+project+"'s dev inbox ("+res.ID+")")
		default:
			parts = append(parts, "email to "+to+" via "+project+" ("+res.Status+")")
		}
	}
	if len(parts) == 0 {
		return "not delivered: set a webhook or an email project (tiffin observe settings set --webhook URL or --email-project NAME)"
	}
	return strings.Join(parts, "; ")
}

// alertEmail is the notification as a branded email (packages/emails: alert).
func (a *Alerter) alertEmail(n Notification) (*templates.Email, error) {
	domain := a.Box
	if a.Platform != nil && a.Platform.Domain != "" {
		domain = a.Platform.Domain
	}
	state := n.State
	if state != "firing" && state != "resolved" {
		state = "test"
	}
	watching := n.Subject
	if watching == n.Rule || watching == "test" {
		watching = ""
	}
	reading := ""
	if n.Threshold != 0 || n.Value != 0 {
		reading = fmt.Sprintf("%g (limit %g)", n.Value, n.Threshold)
	}
	return templates.Alert(templates.AlertData{Brand: templates.Brand(domain), Host: templates.Host(n.Dashboard, "dashboard."+domain),
		MarkURL: templates.MarkURL(n.Dashboard), State: state, Box: n.Box, Rule: n.Rule, Summary: n.Summary, Watching: watching,
		Value: reading, When: templates.When(n.At), Description: n.Description, URL: n.Dashboard})
}

func notificationText(n Notification) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nRule: %s (%s)\nSubject: %s\nState: %s\nValue: %g (threshold %g)\nAt: %s\n",
		n.Summary, n.Rule, n.Kind, n.Subject, n.State, n.Value, n.Threshold, n.At.Format(time.RFC1123))
	if n.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", n.Description)
	}
	if n.Dashboard != "" {
		fmt.Fprintf(&b, "\nDashboard: %s\n", n.Dashboard)
	}
	return b.String()
}

// postWebhook sends n to hook. Its errors never contain the URL: a chat
// webhook's path is its secret, and delivery errors are kept in the alert
// history that every reader of the box sees.
func postWebhook(ctx context.Context, hook string, n Notification) error {
	body, _ := json.Marshal(n)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(body))
	if err != nil {
		return errors.New("the webhook URL is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tiffin-alerts")
	res, err := httpc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the same error without "Post <url>:"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("no answer within 10s")
		}
		return errors.New(strings.ReplaceAll(err.Error(), hook, "<webhook>"))
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}

// Test sends a test notification through every channel.
func (a *Alerter) Test(ctx context.Context) HistoryEntry {
	n := Notification{Box: a.Box, Rule: "test", Kind: "test", Subject: "test", State: "test", Summary: "Test alert from " + a.Box + ": delivery works.", At: time.Now().UTC()}
	n.Text = "[TEST] " + n.Summary
	d := a.deliver(ctx, n)
	h := HistoryEntry{At: n.At, Rule: "test", Subject: "test", State: "test", Summary: n.Summary, Delivery: d}
	a.Store.addHistory(ctx, h)
	return h
}

func bytesH(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
