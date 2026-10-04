// Package email is the Tiffin email module: transactional mail for every
// project, with a dev inbox that catches everything until the box has a relay.
//
//   - Apps send through SMTP (env SMTP_URL, per-project credentials) or the
//     API (POST /v1/projects/{project}/email/send). Tiffin's own submission
//     server listens on 127.0.0.1:2525 (and the runtime's host address, when
//     the runtime publishes one).
//   - With no relay configured — and always for preview projects — messages
//     land in the dev inbox: the raw RFC 5322 file under
//     /var/lib/tiffin/email/messages/<project>/ and metadata in the state DB.
//   - With a relay (PUT /v1/email/relay) messages are queued and delivered
//     with retries and exponential backoff. Hard bounces go on the project's
//     suppression list; suppressed recipients are never sent to.
//   - Each project has a send rate limit (default 300 per hour).
package email

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/backup"
	"github.com/btahir/tiffin/internal/platform"
)

var mod = &Module{}

func init() {
	platform.Register(mod)
	// Captured messages (raw .eml files); their metadata is in the state DB,
	// which every backup set already copies.
	backup.Include("email", "/var/lib/tiffin/email")
}

const (
	// SMTPPort is the submission port of Tiffin's own SMTP server.
	SMTPPort = 2525
	// DefaultRatePerHour is each project's send limit until the owner sets one.
	DefaultRatePerHour = 300
	// MaxMessageBytes is the largest message accepted.
	MaxMessageBytes = 25 << 20
	// InboxLimit is how many messages a project's dev inbox keeps (oldest go first).
	InboxLimit = 1000
	// MaxAttempts is how many times a relay delivery is tried.
	MaxAttempts = 8
)

// Module implements the email module.
type Module struct {
	mu      sync.Mutex
	smtpd   *smtpServer
	limiter *limiter
	hub     *hub
	queue   Queue
	wake    chan struct{}

	// Tests override these.
	smtpAddr string
	extraIP  func(ctx context.Context, p *platform.Platform) string
	backoff  func(attempt int) time.Duration
}

func (*Module) Name() string { return "email" }
func (*Module) Order() int   { return 20 }

// Queue hands queued relay deliveries to a worker. The default runs in this
// process and polls the state DB; the queue module can take over by calling
// SetQueue and invoking Deliver for each job it runs.
type Queue interface {
	// Enqueue schedules delivery of message id no earlier than at.
	Enqueue(ctx context.Context, id string, at time.Time)
}

// SetQueue replaces the in-process delivery queue.
func SetQueue(q Queue) {
	mod.mu.Lock()
	mod.queue = q
	mod.mu.Unlock()
}

// IsPreview reports whether a project is a preview deployment, whose mail
// always goes to the dev inbox. The deploy module may replace it; by default
// a project is a preview when its env TIFFIN_ENVIRONMENT is "preview".
var IsPreview = func(ctx context.Context, p *platform.Platform, project string) bool {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return false
	}
	r, ok := res[change.KindEnv+"/TIFFIN_ENVIRONMENT"]
	if !ok {
		return false
	}
	var v string
	_ = json.Unmarshal(r.Spec, &v)
	return v == "preview"
}

func (m *Module) state() (*limiter, *hub) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.limiter == nil {
		m.limiter = newLimiter()
		m.hub = newHub()
		m.wake = make(chan struct{}, 1)
	}
	return m.limiter, m.hub
}

// ---- per-project settings ----

// projectSettings is what the email reconciler records per project.
type projectSettings struct {
	From string `json:"from"`
}

func (m *Module) settings(ctx context.Context, p *platform.Platform, project string) (*projectSettings, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "project/"+project)
	if err != nil || !ok {
		return nil, err
	}
	var s projectSettings
	return &s, json.Unmarshal(raw, &s)
}

// defaultFrom is "<project>@<box domain>".
func defaultFrom(p *platform.Platform, project string) string { return project + "@" + p.Domain }

func smtpSecretName(project string) string {
	return "SMTP_" + strings.ToUpper(strings.ReplaceAll(project, "-", "_"))
}

func randomSecret() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// smtpPassword returns the project's SMTP password ("" when it has none).
func smtpPassword(ctx context.Context, p *platform.Platform, project string) (string, error) {
	if p.Secrets == nil {
		return "", errors.New("email: the box secret store is not available")
	}
	all, err := p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return "", err
	}
	return all[smtpSecretName(project)], nil
}

// checkLogin verifies SMTP credentials. The username is the project slug,
// optionally "+<preview>" for a preview deployment (same password).
func checkLogin(ctx context.Context, p *platform.Platform, user, pass string) bool {
	user, _, _ = strings.Cut(user, "+")
	if user == "" || pass == "" {
		return false
	}
	want, err := smtpPassword(ctx, p, user)
	if err != nil || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(pass)) == 1
}

// ---- reconcile ----

func (*Module) Kinds() []string { return []string{change.KindService + "/email"} }

// Reconcile provisions a project's SMTP credentials and from address.
func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, _ string, spec json.RawMessage) error {
	if spec == nil {
		if p.Secrets != nil {
			if _, err := p.Secrets.Delete(ctx, secretsProject, smtpSecretName(project)); err != nil {
				return err
			}
		}
		return p.DB.KVDelete(ctx, kvNS, "project/"+project)
	}
	var e manifest.Email
	if err := json.Unmarshal(spec, &e); err != nil {
		return err
	}
	pw, err := smtpPassword(ctx, p, project)
	if err != nil {
		return err
	}
	if pw == "" {
		if err := p.Secrets.Set(ctx, secretsProject, smtpSecretName(project), randomSecret(), "email"); err != nil {
			return err
		}
	}
	from := e.From
	if from == "" {
		from = defaultFrom(p, project)
	}
	raw, _ := json.Marshal(projectSettings{From: from})
	return p.DB.KVPut(ctx, kvNS, "project/"+project, raw)
}

// ---- env ----

func (m *Module) containerHost(ctx context.Context, p *platform.Platform) string {
	if m.extraIP != nil {
		if ip := m.extraIP(ctx, p); ip != "" {
			return ip
		}
		return "127.0.0.1"
	}
	if raw, ok, _ := p.DB.KVGet(ctx, "runtime", "host-ip"); ok && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw))
	}
	return "127.0.0.1"
}

func (m *Module) smtpPort() int {
	if m.smtpAddr != "" {
		if _, port, err := net.SplitHostPort(m.smtpAddr); err == nil {
			n, _ := strconv.Atoi(port)
			return n
		}
	}
	return SMTPPort
}

// Env gives a project's apps SMTP settings for Tiffin's submission server.
func (m *Module) Env(ctx context.Context, p *platform.Platform, project, _ string) (map[string]string, error) {
	s, err := m.settings(ctx, p, project)
	if err != nil || s == nil {
		return nil, err
	}
	pw, err := smtpPassword(ctx, p, project)
	if err != nil || pw == "" {
		return nil, err
	}
	host := m.containerHost(ctx, p)
	port := strconv.Itoa(m.smtpPort())
	u := url.URL{Scheme: "smtp", User: url.UserPassword(project, pw), Host: net.JoinHostPort(host, port)}
	return map[string]string{
		"SMTP_URL":      u.String(),
		"SMTP_HOST":     host,
		"SMTP_PORT":     port,
		"SMTP_USER":     project,
		"SMTP_USERNAME": project,
		"SMTP_PASSWORD": pw,
		"SMTP_PASS":     pw,
		"SMTP_SECURE":   "false",
		"EMAIL_FROM":    s.From,
	}, nil
}

// ---- the pipeline ----

// Result is what happened to a message.
type Result struct {
	ID         string   `json:"id" doc:"Message ID (msg_...)"`
	Delivery   string   `json:"delivery" enum:"inbox,relay,suppressed"`
	Status     string   `json:"status" enum:"captured,queued,suppressed"`
	Reason     string   `json:"reason" doc:"Why it went where it went"`
	Recipients []string `json:"recipients" doc:"Envelope recipients it goes to"`
	Suppressed []string `json:"suppressed,omitempty" doc:"Recipients dropped: on the suppression list"`
}

// RateLimitError means the project sent too much too fast.
type RateLimitError struct {
	PerHour    int
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("send rate limit reached (%d messages per hour); retry in %s", e.PerHour, e.RetryAfter.Round(time.Second))
}

var errNoProject = errors.New("email is not enabled for this project")

// route decides where a project's mail goes now. preview names the
// preview deployment the message came from ("" for production).
func (m *Module) route(ctx context.Context, p *platform.Platform, project, preview string) (string, string, error) {
	if preview != "" {
		return DeliveryInbox, "sent by preview " + preview + ": preview mail is always captured in the dev inbox", nil
	}
	if IsPreview(ctx, p, project) {
		return DeliveryInbox, "preview project: mail is always captured in the dev inbox", nil
	}
	r, err := getRelay(ctx, p)
	if err != nil {
		return "", "", err
	}
	if r == nil {
		return DeliveryInbox, "no SMTP relay is configured on this box, so mail is captured in the dev inbox", nil
	}
	return DeliveryRelay, "sent through the relay " + r.Host, nil
}

// WillSend reports whether a project's mail would leave the box now (a
// relay is set up and the project is not a preview). Auth uses it to decide
// whether new users must confirm their address.
func (m *Module) WillSend(ctx context.Context, p *platform.Platform, project string) (bool, error) {
	return m.willSend(ctx, p, project)
}

// willSend reports whether a project's mail would leave the box now.
func (m *Module) willSend(ctx context.Context, p *platform.Platform, project string) (bool, error) {
	d, _, err := m.route(ctx, p, project, "")
	return d == DeliveryRelay, err
}

// accept takes a finished raw message from the API or SMTP and routes it.
func (m *Module) accept(ctx context.Context, p *platform.Platform, project, preview, source, id, mailFrom string, rcpt []string, raw []byte) (*Result, error) {
	if err := ensureSchema(ctx, p.DB.SQL()); err != nil {
		return nil, err
	}
	if len(raw) > MaxMessageBytes {
		return nil, invalid("message is %d bytes; the limit is %d", len(raw), MaxMessageBytes)
	}
	lim, h := m.state()
	rate, err := ratePerHour(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if wait := lim.take(project, rate, time.Now()); wait > 0 {
		return nil, &RateLimitError{PerHour: rate, RetryAfter: wait}
	}
	sup, err := suppressed(ctx, p.DB.SQL(), project, rcpt)
	if err != nil {
		return nil, err
	}
	var keep, dropped []string
	seen := map[string]bool{}
	for _, r := range rcpt {
		n := normAddr(r)
		if seen[n] {
			continue
		}
		seen[n] = true
		if sup[n] {
			dropped = append(dropped, r)
		} else {
			keep = append(keep, r)
		}
	}
	parsed, err := parse(raw)
	if err != nil {
		return nil, invalid("message could not be parsed: %v", err)
	}
	delivery, reason, err := m.route(ctx, p, project, preview)
	if err != nil {
		return nil, err
	}
	status := StatusCaptured
	switch {
	case len(keep) == 0:
		delivery, status, reason = DeliverySuppressed, StatusSuppressed, "every recipient is on the suppression list; nothing was sent"
	case delivery == DeliveryRelay:
		status = StatusQueued
	}
	now := time.Now().UTC()
	rec := &record{Summary: Summary{ID: id, Project: project, CreatedAt: now, Source: source, From: parsed.From, To: parsed.To,
		Subject: parsed.Subject, Snippet: parsed.snippet(), Size: int64(len(raw)), Attachments: len(parsed.Attachments),
		Delivery: delivery, Status: status, Reason: reason, Suppressed: dropped}, MailFrom: mailFrom, Rcpt: keep}
	if status == StatusQueued {
		rec.NextAttempt = now
	}
	if status != StatusSuppressed {
		if err := writeRaw(p.DataRoot, project, id, raw); err != nil {
			return nil, err
		}
	}
	if err := insertRecord(ctx, p.DB.SQL(), rec); err != nil {
		_ = os.Remove(rawPath(p.DataRoot, project, id))
		return nil, err
	}
	switch status {
	case StatusQueued:
		m.enqueue(ctx, id, now)
	case StatusCaptured:
		m.trimInbox(ctx, p, project)
	}
	h.publish(project, rec.Summary)
	if keep == nil {
		keep = []string{}
	}
	return &Result{ID: id, Delivery: delivery, Status: status, Reason: reason, Recipients: keep, Suppressed: dropped}, nil
}

// trimInbox keeps a project's inbox at InboxLimit messages.
func (m *Module) trimInbox(ctx context.Context, p *platform.Platform, project string) {
	rows, err := p.DB.SQL().QueryContext(ctx, `SELECT id FROM email_messages WHERE project = ? AND delivery = ? ORDER BY id DESC LIMIT -1 OFFSET ?`,
		project, DeliveryInbox, InboxLimit)
	if err != nil {
		return
	}
	var old []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			old = append(old, id)
		}
	}
	rows.Close()
	for _, id := range old {
		_, _ = deleteMessages(ctx, p, project, id)
	}
}

// Send composes and sends (or captures) a message for a project. Other
// modules (auth: magic links, sign-in codes) call it directly.
func Send(ctx context.Context, p *platform.Platform, project string, msg Message) (*Result, error) {
	return mod.send(ctx, p, project, "api", msg)
}

func (m *Module) send(ctx context.Context, p *platform.Platform, project, source string, msg Message) (*Result, error) {
	s, err := m.settings(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errNoProject
	}
	id := ids.New("msg")
	raw, from, rcpt, err := compose(msg, s.From, id, p.Domain, time.Now())
	if err != nil {
		return nil, err
	}
	return m.accept(ctx, p, project, "", source, id, from, rcpt, raw)
}

// PreviewSMTPURL turns a project's SMTP_URL into the one a preview
// deployment should get: the username becomes "<project>+<preview>", which
// makes every message it sends land in the dev inbox, relay or not.
func PreviewSMTPURL(smtpURL, preview string) string {
	u, err := url.Parse(smtpURL)
	if err != nil || u.User == nil || preview == "" {
		return smtpURL
	}
	pw, _ := u.User.Password()
	user, _, _ := strings.Cut(u.User.Username(), "+")
	u.User = url.UserPassword(user+"+"+preview, pw)
	return u.String()
}

// ---- rate limits ----

func ratePerHour(ctx context.Context, p *platform.Platform, project string) (int, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "rate/"+project)
	if err != nil || !ok {
		return DefaultRatePerHour, err
	}
	return strconv.Atoi(string(raw))
}

// limiter is a token bucket per project: capacity min(perHour, 60),
// refilled at perHour per hour.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*bucket{}} }

// take spends one token; it returns 0, or how long until one is available.
func (l *limiter) take(project string, perHour int, now time.Time) time.Duration {
	if perHour <= 0 {
		return 0 // unlimited
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	capacity := float64(min(perHour, 60))
	rate := float64(perHour) / 3600 // tokens per second
	b, ok := l.buckets[project]
	if !ok {
		b = &bucket{tokens: capacity, at: now}
		l.buckets[project] = b
	}
	b.tokens = min(capacity, b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return time.Duration((1 - b.tokens) / rate * float64(time.Second))
}

// ---- start and health ----

// Start runs the SMTP submission server, the delivery worker and housekeeping.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	if err := ensureSchema(ctx, p.DB.SQL()); err != nil {
		return err
	}
	m.state()
	if err := os.MkdirAll(messagesDir(p.DataRoot), 0o700); err != nil {
		return err
	}
	srv := &smtpServer{m: m, p: p}
	if err := srv.start(ctx); err != nil {
		return fmt.Errorf("email: SMTP server: %w", err)
	}
	m.mu.Lock()
	m.smtpd = srv
	if m.queue == nil {
		m.queue = &localQueue{m: m}
	}
	m.mu.Unlock()
	go m.worker(ctx, p)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			m.housekeep(ctx, p)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

// housekeep drops relayed messages' raw files after 7 days and their log
// rows after 30.
func (m *Module) housekeep(ctx context.Context, p *platform.Platform) {
	db := p.DB.SQL()
	week := ts(time.Now().Add(-7 * 24 * time.Hour))
	rows, err := db.QueryContext(ctx, `SELECT id, project FROM email_messages WHERE delivery = ? AND status IN (?, ?) AND created_at < ?`,
		DeliveryRelay, StatusSent, StatusFailed, week)
	if err == nil {
		for rows.Next() {
			var id, project string
			if rows.Scan(&id, &project) == nil {
				_ = os.Remove(rawPath(p.DataRoot, project, id))
			}
		}
		rows.Close()
	}
	_, _ = db.ExecContext(ctx, `DELETE FROM email_messages WHERE delivery != ? AND status IN (?, ?, ?) AND created_at < ?`,
		DeliveryInbox, StatusSent, StatusFailed, StatusSuppressed, ts(time.Now().Add(-30*24*time.Hour)))
}

// Checks reports the SMTP server and the delivery mode.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	m.mu.Lock()
	srv := m.smtpd
	m.mu.Unlock()
	if srv == nil {
		return []platform.Check{{Name: "email", OK: false, Detail: "the SMTP submission server is not running"}}
	}
	mode := "dev inbox (no relay configured)"
	if r, err := getRelay(ctx, p); err == nil && r != nil {
		mode = "relay " + r.Host
	}
	detail := "SMTP on " + strings.Join(srv.addrs(), ", ") + "; mail goes to the " + mode
	if n := failedCount(ctx, p); n > 0 {
		detail += fmt.Sprintf("; %d deliveries failed in the last day", n)
	}
	return []platform.Check{{Name: "email", OK: true, Detail: detail}}
}

func failedCount(ctx context.Context, p *platform.Platform) int {
	var n int
	_ = p.DB.SQL().QueryRowContext(ctx, `SELECT count(*) FROM email_messages WHERE status = ? AND created_at > ?`,
		StatusFailed, ts(time.Now().Add(-24*time.Hour))).Scan(&n)
	return n
}
