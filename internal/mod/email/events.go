package email

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// ---- marking outgoing mail so events can find it ----

// headerField is one header of a raw message, with its folded lines.
type headerField struct {
	name string // canonical
	raw  []byte // the field's bytes, line endings included
}

// splitHeader splits a raw message into its header fields and the rest
// (the blank line and body).
func splitHeader(raw []byte) ([]headerField, []byte) {
	var fields []headerField
	i := 0
	for i < len(raw) {
		j := bytes.IndexByte(raw[i:], '\n')
		end := len(raw)
		if j >= 0 {
			end = i + j + 1
		}
		line := raw[i:end]
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			return fields, raw[i:]
		}
		if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
			f := &fields[len(fields)-1]
			f.raw = append(f.raw, line...)
		} else {
			name, _, _ := bytes.Cut(line, []byte(":"))
			fields = append(fields, headerField{name: textproto.CanonicalMIMEHeaderKey(string(bytes.TrimSpace(name))), raw: append([]byte(nil), line...)})
		}
		i = end
	}
	return fields, nil
}

func (f headerField) value() string {
	_, v, _ := bytes.Cut(f.raw, []byte(":"))
	return strings.Join(strings.Fields(string(v)), " ")
}

// markForRelay adds what lets the provider's events be matched back to the
// message: our ID as a header (and, for SendGrid, a unique argument; for
// Postmark, metadata), and a Message-ID when the message has none. It
// returns the message and its Message-ID. Correlation fields the sender put
// in itself are dropped first: events trust them, so only the box sets them.
func markForRelay(raw []byte, id, domain, provider string) ([]byte, string) {
	fields, rest := splitHeader(raw)
	var add bytes.Buffer
	msgID := ""
	if i := slices.IndexFunc(fields, func(f headerField) bool { return f.name == "Message-Id" }); i >= 0 {
		msgID = normMessageID(fields[i].value())
	} else {
		msgID = "<" + id + "@" + domain + ">"
		add.WriteString("Message-ID: " + msgID + "\r\n")
	}
	add.WriteString("X-Tiffin-Message-Id: " + id + "\r\n")
	var smtpAPI []string // the sender's X-SMTPAPI headers
	fields = slices.DeleteFunc(fields, func(f headerField) bool {
		switch {
		case f.name == "X-Tiffin-Message-Id":
			return true
		case provider == ProviderSendGrid && f.name == "X-Smtpapi":
			smtpAPI = append(smtpAPI, f.value())
			return true
		case provider == ProviderPostmark && strings.EqualFold(f.name, "X-Pm-Metadata-"+postmarkMetaKey):
			return true
		}
		return false
	})
	switch provider {
	case ProviderSendGrid:
		// X-SMTPAPI unique_args come back as top-level fields of every event.
		// The sender's own header (the first one that is JSON) is kept, with
		// our ID set in it.
		args := map[string]any{}
		for _, v := range smtpAPI {
			if json.Unmarshal([]byte(v), &args) == nil {
				break
			}
			args = map[string]any{}
		}
		ua, _ := args["unique_args"].(map[string]any)
		if ua == nil {
			ua = map[string]any{}
		}
		ua[tiffinArg] = id
		args["unique_args"] = ua
		b, _ := json.Marshal(args)
		if len(b) > 980 { // SendGrid: keep the header line under 1,000 characters
			b, _ = json.Marshal(map[string]any{"unique_args": map[string]any{tiffinArg: id}})
		}
		add.WriteString("X-SMTPAPI: " + string(b) + "\r\n")
	case ProviderPostmark:
		add.WriteString("X-PM-Metadata-" + postmarkMetaKey + ": " + id + "\r\n")
	}
	out := make([]byte, 0, len(raw)+add.Len())
	out = append(out, add.Bytes()...)
	for _, f := range fields {
		out = append(out, f.raw...)
	}
	out = append(out, rest...)
	return out, msgID
}

var (
	uuidRE   = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	queuedRE = regexp.MustCompile(`(?i)queued as ([A-Za-z0-9_-]{6,})`)
)

// providerIDFromReply picks the provider's message ID out of its reply to
// DATA, when it gives one ("queued as ...", or a UUID).
func providerIDFromReply(reply string) string {
	if m := queuedRE.FindStringSubmatch(reply); m != nil {
		return m[1]
	}
	return uuidRE.FindString(reply)
}

func track(ctx context.Context, db *sql.DB, id, project, provider, headerID string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO email_tracking(id, project, provider, header_id, provider_id, sent_at) VALUES (?,?,?,?,'',?)
		ON CONFLICT(id) DO UPDATE SET provider = excluded.provider, header_id = excluded.header_id, sent_at = excluded.sent_at`,
		id, project, provider, headerID, ts(time.Now()))
	return err
}

// ---- matching events to messages ----

var msgIDRE = regexp.MustCompile(`^msg_[0-9A-Z]{26}$`)

// matchEvent finds the message an event is about: by our ID, then the
// Message-ID header, then the provider's message ID, then (once, for the
// first event of a message from a provider that echoes none of those) by
// recipient and subject within an hour of sending. A message only matches
// when it went through this provider and the event's recipient is one of
// its recipients. The Message-ID is the sender's to choose, so one shared by
// messages of different projects matches none of them.
func matchEvent(ctx context.Context, db *sql.DB, provider string, ev *inEvent) (id, project string) {
	const sel = `SELECT m.id, m.project, m.rcpt FROM email_tracking t JOIN email_messages m ON m.id = t.id WHERE t.provider = ? AND `
	one := func(q string, args ...any) bool {
		rows, err := db.QueryContext(ctx, sel+q+` ORDER BY t.sent_at DESC LIMIT 20`, append([]any{provider}, args...)...)
		if err != nil {
			return false
		}
		defer rows.Close()
		var hits [][2]string
		for rows.Next() {
			var mid, proj, rcpt string
			if rows.Scan(&mid, &proj, &rcpt) != nil || !hasRecipient(rcpt, ev.Recipient) {
				continue
			}
			hits = append(hits, [2]string{mid, proj})
		}
		if len(hits) == 0 || slices.ContainsFunc(hits, func(h [2]string) bool { return h[1] != hits[0][1] }) {
			return false // none, or messages of different projects: better unmatched than wrong
		}
		id, project = hits[0][0], hits[0][1] // the newest
		return true
	}
	if msgIDRE.MatchString(ev.TiffinID) && one(`m.id = ?`, ev.TiffinID) {
		return
	}
	if ev.HeaderID != "" && one(`t.header_id = ?`, ev.HeaderID) {
		return
	}
	if ev.ProviderID != "" && one(`t.provider_id = ?`, ev.ProviderID) {
		return
	}
	if ev.Subject == "" || ev.Recipient == "" || ev.At.IsZero() {
		return "", ""
	}
	rows, err := db.QueryContext(ctx, `SELECT m.id, m.project, m.rcpt FROM email_messages m JOIN email_tracking t ON t.id = m.id
		WHERE t.provider = ? AND t.provider_id = '' AND m.subject = ? AND t.sent_at BETWEEN ? AND ?`,
		provider, ev.Subject, ts(ev.At.Add(-time.Hour)), ts(ev.At.Add(5*time.Minute)))
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	var hits [][2]string
	for rows.Next() {
		var mid, proj, rcpt string
		if rows.Scan(&mid, &proj, &rcpt) != nil {
			continue
		}
		if hasRecipient(rcpt, ev.Recipient) {
			hits = append(hits, [2]string{mid, proj})
		}
	}
	if len(hits) != 1 {
		return "", "" // none, or ambiguous: better unmatched than wrong
	}
	return hits[0][0], hits[0][1]
}

// hasRecipient reports whether addr ("" for an event without one) is in a
// message's JSON recipient list.
func hasRecipient(rcpt, addr string) bool {
	if addr == "" {
		return true
	}
	var list []string
	_ = json.Unmarshal([]byte(rcpt), &list)
	return slices.ContainsFunc(list, func(a string) bool { return normAddr(a) == normAddr(addr) })
}

// statusRank orders relayed statuses: an event only moves a message forward.
var statusRank = map[string]int{StatusQueued: 0, StatusSent: 1, StatusDelivered: 2, StatusBounced: 3, StatusFailed: 3, StatusComplained: 4}

// eventStatus is the message status an event implies ("" = none).
func eventStatus(ev *inEvent) string {
	switch ev.Type {
	case EventDelivered:
		return StatusDelivered
	case EventBounced, EventDropped:
		return StatusBounced
	case EventComplained:
		return StatusComplained
	case EventFailed:
		return StatusFailed
	}
	return ""
}

var suppressWords = map[string]string{"bounce": "bounced", "complaint": "marked it as spam", "unsubscribe": "unsubscribed"}

// applyEvents records verified events. It returns how many were new and
// matched a message.
func (m *Module) applyEvents(ctx context.Context, p *platform.Platform, provider string, evs []inEvent) (int, error) {
	db := p.DB.SQL()
	name := provider
	if pr := PresetByID(provider); pr != nil {
		name = pr.Name
	}
	matched := 0
	touched := map[string]string{} // message → project
	for i := range evs {
		ev := &evs[i]
		id, project := matchEvent(ctx, db, provider, ev)
		if id == "" {
			continue
		}
		isNew, err := applyEvent(ctx, db, name, provider, ev, id, project)
		if err != nil {
			return matched, err
		}
		if isNew {
			matched++
			touched[id] = project
		}
	}
	_, h := m.state()
	for id, project := range touched {
		if s, ok := m.summary(ctx, p, project, id); ok {
			h.publish(project, s)
		}
	}
	return matched, nil
}

// applyEvent records one matched event and everything it implies (the
// provider's ID, the message's status, a suppression) in one transaction,
// so an event is only ever marked seen together with its effects: a
// failure leaves nothing behind, and the provider's retry applies it all.
// It reports whether the event was new.
func applyEvent(ctx context.Context, db *sql.DB, name, provider string, ev *inEvent, id, project string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO email_events(provider, key, project, message, type, recipient, detail, hard, at, received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider, key) DO NOTHING`,
		provider, ev.Key, project, id, ev.Type, ev.Recipient, ev.Detail, ev.Hard, ts(ev.At), ts(time.Now()))
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err // seen before: a retry or a replay
	}
	if ev.ProviderID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE email_tracking SET provider_id = ? WHERE id = ? AND provider_id = ''`, ev.ProviderID, id); err != nil {
			return false, err
		}
	}
	if st := eventStatus(ev); st != "" {
		var cur string
		switch err := tx.QueryRowContext(ctx, `SELECT status FROM email_messages WHERE id = ?`, id).Scan(&cur); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return false, err
		default:
			if r, ok := statusRank[cur]; ok && statusRank[st] > r {
				lastErr := ""
				if st == StatusBounced || st == StatusFailed {
					lastErr = strings.TrimSpace(ev.Recipient + ": " + ev.Detail)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE email_messages SET status = ?, last_error = CASE WHEN ? != '' THEN ? ELSE last_error END WHERE id = ?`,
					st, lastErr, lastErr, id); err != nil {
					return false, err
				}
			}
		}
	}
	if ev.suppress != "" && ev.Recipient != "" {
		detail := name + " reported the address " + suppressWords[ev.suppress]
		if ev.Detail != "" {
			detail += ": " + ev.Detail
		}
		if err := addSuppression(ctx, tx, project, Suppression{Address: ev.Recipient, Reason: ev.suppress, Detail: clip(detail, 480)}); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// messageEvents lists a message's events, oldest first.
func messageEvents(ctx context.Context, db *sql.DB, id string) ([]Event, error) {
	rows, err := db.QueryContext(ctx, `SELECT provider, type, recipient, detail, hard, at FROM email_events WHERE message = ? ORDER BY at, seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&e.Provider, &e.Type, &e.Recipient, &e.Detail, &e.Hard, &at); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- the webhook's state ----

type hookState struct {
	ConfiguredAt   time.Time `json:"configuredAt,omitzero"`
	ConfiguredBy   string    `json:"configuredBy,omitempty"`
	LastEventAt    time.Time `json:"lastEventAt,omitzero"`
	Requests       int       `json:"requests"`
	Matched        int       `json:"matched"`
	LastRejectedAt time.Time `json:"lastRejectedAt,omitzero"`
	LastRejection  string    `json:"lastRejection,omitempty"`
}

func (m *Module) hookState(ctx context.Context, p *platform.Platform, provider string) hookState {
	var s hookState
	if raw, ok, _ := p.DB.KVGet(ctx, kvNS, "webhook/"+provider); ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func (m *Module) updateHook(ctx context.Context, p *platform.Platform, provider string, f func(*hookState)) {
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	s := m.hookState(ctx, p, provider)
	f(&s)
	raw, _ := json.Marshal(s)
	_ = p.DB.KVPut(ctx, kvNS, "webhook/"+provider, raw)
}

// Webhook is how far a provider's delivery events are set up.
type Webhook struct {
	Provider string `json:"provider" enum:"sendgrid,resend,postmark"`
	URL      string `json:"url" doc:"The address to paste in the provider's webhook settings"`
	User     string `json:"user,omitempty" doc:"Postmark: the user name in the address (the password is shown once, when it is made)"`
	Public   bool   `json:"public" doc:"false when the dashboard's address cannot be reached from the internet, so the provider cannot deliver"`
	KeySet   bool   `json:"keySet" doc:"Whether the verification key or signing secret is saved (it is never shown)"`
	// Receiving: a verified request arrived since the key was saved.
	Receiving      bool      `json:"receiving"`
	ConfiguredAt   time.Time `json:"configuredAt,omitzero"`
	LastEventAt    time.Time `json:"lastEventAt,omitzero" doc:"When the last verified request arrived"`
	Requests       int       `json:"requests" doc:"Verified requests since the key was saved"`
	Matched        int       `json:"matched" doc:"Events matched to a message since the key was saved"`
	LastRejectedAt time.Time `json:"lastRejectedAt,omitzero"`
	LastRejection  string    `json:"lastRejection,omitempty" doc:"Why the last refused request was refused"`
}

// publicAddress reports whether a dashboard URL can be reached from the
// internet: not localhost, .localhost, .test, .internal or a private IP.
func publicAddress(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, s := range []string{"localhost", ".localhost", ".test", ".internal", ".local", ".lan", ".home.arpa"} {
		if h == strings.TrimPrefix(s, ".") || strings.HasSuffix(h, s) {
			return false
		}
	}
	if ip := net.ParseIP(h); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
	}
	return true
}

func eventsPath(provider string) string { return "/v1/email/events/" + provider }

func (m *Module) webhook(ctx context.Context, p *platform.Platform, provider string) Webhook {
	w := Webhook{Provider: provider, URL: strings.TrimSuffix(p.PublicURL, "/") + eventsPath(provider), Public: publicAddress(p.PublicURL)}
	if provider == ProviderPostmark {
		w.User = postmarkUser
	}
	key, _ := webhookKey(ctx, p, provider)
	w.KeySet = key != ""
	s := m.hookState(ctx, p, provider)
	w.ConfiguredAt, w.LastEventAt, w.Requests, w.Matched = s.ConfiguredAt, s.LastEventAt, s.Requests, s.Matched
	w.LastRejectedAt, w.LastRejection = s.LastRejectedAt, s.LastRejection
	w.Receiving = w.KeySet && !s.LastEventAt.IsZero() && !s.LastEventAt.Before(s.ConfiguredAt)
	return w
}

// webhooks lists the event setups worth showing: the relay's provider's,
// and any other with a key saved.
func (m *Module) webhooks(ctx context.Context, p *platform.Platform, relay *Relay) []Webhook {
	out := []Webhook{}
	for _, pr := range eventProviders {
		key, _ := webhookKey(ctx, p, pr)
		if key != "" || (relay != nil && relay.provider() == pr) {
			out = append(out, m.webhook(ctx, p, pr))
		}
	}
	return out
}

func webhookKey(ctx context.Context, p *platform.Platform, provider string) (string, error) {
	if p.Secrets == nil {
		return "", nil
	}
	all, err := p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return "", err
	}
	return all[webhookSecretName(provider)], nil
}

// ---- the public endpoint ----

// handleEvents serves POST /v1/email/events/{provider}. It carries no
// Tiffin credentials: the provider's signature (or Postmark's password) is
// the check, and nothing is read before it passes.
func (m *Module) handleEvents(p *platform.Platform, provider string, w http.ResponseWriter, req *http.Request) {
	reply := func(status int, msg string, extra map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		body := map[string]any{"message": msg}
		for k, v := range extra {
			body[k] = v
		}
		_ = json.NewEncoder(w).Encode(body)
	}
	if p == nil {
		reply(http.StatusServiceUnavailable, "email is only available on a box", nil)
		return
	}
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		reply(http.StatusMethodNotAllowed, "events are POSTed", nil)
		return
	}
	ctx := req.Context()
	if err := ensureSchema(ctx, p.DB.SQL()); err != nil {
		reply(http.StatusInternalServerError, "the box could not open its email tables", nil)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxEventBody+1))
	if err != nil || len(body) > maxEventBody {
		reply(http.StatusRequestEntityTooLarge, "the request is too large", nil)
		return
	}
	refuse := func(status int, why string) {
		p.Log.Warn("email: event webhook refused", "provider", provider, "why", why, "from", req.RemoteAddr)
		m.updateHook(context.WithoutCancel(ctx), p, provider, func(s *hookState) {
			s.LastRejectedAt, s.LastRejection = time.Now().UTC(), why
		})
		reply(status, why, nil)
	}
	key, err := webhookKey(ctx, p, provider)
	if err != nil {
		reply(http.StatusInternalServerError, "the box could not read its secrets", nil)
		return
	}
	if key == "" {
		refuse(http.StatusUnauthorized, "no key is saved on the box for "+provider+" events yet")
		return
	}
	if err := verify(provider, key, req, body, time.Now()); err != nil {
		refuse(http.StatusUnauthorized, err.Error())
		return
	}
	evs, err := parseEvents(provider, body, header(req, "svix-id", "webhook-id"))
	if err != nil {
		refuse(http.StatusBadRequest, "signed, but unreadable: "+err.Error())
		return
	}
	n, err := m.applyEvents(context.WithoutCancel(ctx), p, provider, evs)
	if err != nil {
		p.Log.Warn("email: recording events failed", "provider", provider, "err", err)
		reply(http.StatusInternalServerError, "the box could not record the events; send them again", nil)
		return
	}
	m.updateHook(context.WithoutCancel(ctx), p, provider, func(s *hookState) {
		s.LastEventAt = time.Now().UTC()
		s.Requests++
		s.Matched += n
	})
	reply(http.StatusOK, "ok", map[string]any{"events": len(evs), "matched": n})
}
