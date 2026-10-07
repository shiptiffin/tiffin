package email

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// Message delivery routes and statuses.
const (
	DeliveryInbox      = "inbox"      // captured in the dev inbox, never sent
	DeliveryRelay      = "relay"      // sent through the configured SMTP relay
	DeliverySuppressed = "suppressed" // every recipient was on the suppression list

	StatusCaptured   = "captured"
	StatusQueued     = "queued"
	StatusSent       = "sent"
	StatusFailed     = "failed"
	StatusSuppressed = "suppressed"
)

// The email module keeps its tables in the platform state database; they
// are created on first use (CREATE IF NOT EXISTS) so the module owns them.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS email_messages (
		id          TEXT PRIMARY KEY,
		project     TEXT NOT NULL,
		created_at  TEXT NOT NULL,
		source      TEXT NOT NULL,
		mail_from   TEXT NOT NULL,
		rcpt        TEXT NOT NULL,
		from_hdr    TEXT NOT NULL,
		to_hdr      TEXT NOT NULL,
		subject     TEXT NOT NULL,
		snippet     TEXT NOT NULL,
		size        INTEGER NOT NULL,
		attachments INTEGER NOT NULL,
		delivery    TEXT NOT NULL,
		status      TEXT NOT NULL,
		reason      TEXT NOT NULL,
		suppressed  TEXT NOT NULL,
		attempts    INTEGER NOT NULL DEFAULT 0,
		next_at     TEXT NOT NULL DEFAULT '',
		last_error  TEXT NOT NULL DEFAULT '',
		sent_at     TEXT NOT NULL DEFAULT ''
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS email_messages_project ON email_messages(project, delivery, id)`,
	`CREATE INDEX IF NOT EXISTS email_messages_due ON email_messages(status, next_at)`,
	// What the box sent each relayed message as, so the provider's
	// delivery events can be matched back to it.
	`CREATE TABLE IF NOT EXISTS email_tracking (
		id          TEXT PRIMARY KEY,
		project     TEXT NOT NULL,
		provider    TEXT NOT NULL,
		header_id   TEXT NOT NULL,
		provider_id TEXT NOT NULL DEFAULT '',
		sent_at     TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS email_tracking_header ON email_tracking(header_id)`,
	`CREATE INDEX IF NOT EXISTS email_tracking_provider ON email_tracking(provider, provider_id)`,
	// Delivery events from the provider; (provider, key) makes each one count once.
	`CREATE TABLE IF NOT EXISTS email_events (
		seq         INTEGER PRIMARY KEY,
		provider    TEXT NOT NULL,
		key         TEXT NOT NULL,
		project     TEXT NOT NULL,
		message     TEXT NOT NULL,
		type        TEXT NOT NULL,
		recipient   TEXT NOT NULL,
		detail      TEXT NOT NULL,
		hard        INTEGER NOT NULL,
		at          TEXT NOT NULL,
		received_at TEXT NOT NULL,
		UNIQUE (provider, key)
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS email_events_message ON email_events(message, at)`,
	`CREATE TABLE IF NOT EXISTS email_suppressions (
		project    TEXT NOT NULL,
		address    TEXT NOT NULL,
		reason     TEXT NOT NULL,
		detail     TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (project, address)
	) STRICT, WITHOUT ROWID`,
}

var (
	schemaMu   sync.Mutex
	schemaDone = map[*sql.DB]bool{}
)

func ensureSchema(ctx context.Context, db *sql.DB) error {
	schemaMu.Lock()
	defer schemaMu.Unlock()
	if schemaDone[db] {
		return nil
	}
	for _, s := range schema {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	schemaDone[db] = true
	return nil
}

// Summary is one message in a listing.
type Summary struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	CreatedAt   time.Time `json:"createdAt"`
	Source      string    `json:"source" enum:"api,smtp,box" doc:"How it arrived: the send API, SMTP submission, or the box itself (invites, sign-in links)"`
	From        string    `json:"from" doc:"The From header"`
	To          []string  `json:"to" doc:"To and Cc header addresses"`
	Subject     string    `json:"subject"`
	Snippet     string    `json:"snippet" doc:"Start of the text body"`
	Size        int64     `json:"size" doc:"Raw message size in bytes"`
	Attachments int       `json:"attachments"`
	Delivery    string    `json:"delivery" enum:"inbox,relay,suppressed" doc:"inbox: captured, never sent; relay: sent through the SMTP relay; suppressed: every recipient was suppressed"`
	Status      string    `json:"status" enum:"captured,queued,sent,delivered,bounced,complained,failed,suppressed" doc:"sent: the relay accepted it; delivered, bounced, complained: what the relay's provider reported back (needs its webhook)"`
	Reason      string    `json:"reason,omitempty" doc:"Why it went where it went, in plain words"`
	Suppressed  []string  `json:"suppressed,omitempty" doc:"Recipients dropped because they are on the suppression list"`
	Attempts    int       `json:"attempts,omitempty" doc:"Relay delivery attempts so far"`
	NextAttempt time.Time `json:"nextAttempt,omitzero" doc:"When the next relay attempt is due"`
	LastError   string    `json:"lastError,omitempty"`
	SentAt      time.Time `json:"sentAt,omitzero"`
}

type record struct {
	Summary
	MailFrom string
	Rcpt     []string
}

const cols = `id, project, created_at, source, mail_from, rcpt, from_hdr, to_hdr, subject, snippet, size, attachments, delivery, status, reason, suppressed, attempts, next_at, last_error, sent_at`

type scanner interface{ Scan(...any) error }

func scanRecord(s scanner) (*record, error) {
	var r record
	var created, rcpt, to, supp, next, sent string
	if err := s.Scan(&r.ID, &r.Project, &created, &r.Source, &r.MailFrom, &rcpt, &r.From, &to, &r.Subject, &r.Snippet, &r.Size,
		&r.Attachments, &r.Delivery, &r.Status, &r.Reason, &supp, &r.Attempts, &next, &r.LastError, &sent); err != nil {
		return nil, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.NextAttempt, _ = time.Parse(time.RFC3339Nano, next)
	r.SentAt, _ = time.Parse(time.RFC3339Nano, sent)
	_ = json.Unmarshal([]byte(rcpt), &r.Rcpt)
	_ = json.Unmarshal([]byte(to), &r.To)
	_ = json.Unmarshal([]byte(supp), &r.Suppressed)
	if r.To == nil {
		r.To = []string{}
	}
	return &r, nil
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func insertRecord(ctx context.Context, db *sql.DB, r *record) error {
	_, err := db.ExecContext(ctx, `INSERT INTO email_messages(`+cols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Project, ts(r.CreatedAt), r.Source, r.MailFrom, jsonList(r.Rcpt), r.From, jsonList(r.To), r.Subject, r.Snippet, r.Size,
		r.Attachments, r.Delivery, r.Status, r.Reason, jsonList(r.Suppressed), r.Attempts, ts(r.NextAttempt), r.LastError, ts(r.SentAt))
	return err
}

var errNotFound = errors.New("no such message")

func getRecord(ctx context.Context, db *sql.DB, project, id string) (*record, error) {
	r, err := scanRecord(db.QueryRowContext(ctx, `SELECT `+cols+` FROM email_messages WHERE id = ? AND project = ?`, id, project))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return r, err
}

// ListFilter selects messages.
type ListFilter struct {
	Project string
	All     bool   // every message, not just the dev inbox
	Query   string // substring of subject, from, to or snippet
	Before  string // message ID cursor (exclusive)
	Limit   int
}

func listRecords(ctx context.Context, db *sql.DB, f ListFilter) ([]*record, error) {
	q := `SELECT ` + cols + ` FROM email_messages WHERE project = ?`
	args := []any{f.Project}
	if !f.All {
		q += ` AND delivery = ?`
		args = append(args, DeliveryInbox)
	}
	if f.Query != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(f.Query)) + "%"
		q += ` AND (lower(subject) LIKE ? ESCAPE '\' OR lower(from_hdr) LIKE ? ESCAPE '\' OR lower(to_hdr) LIKE ? ESCAPE '\' OR lower(snippet) LIKE ? ESCAPE '\')`
		args = append(args, like, like, like, like)
	}
	if f.Before != "" {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- raw message files ----

func messagesDir(root string) string { return filepath.Join(root, "email", "messages") }

func rawPath(root, project, id string) string {
	return filepath.Join(messagesDir(root), project, id+".eml")
}

func writeRaw(root, project, id string, raw []byte) error {
	dir := filepath.Join(messagesDir(root), project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := rawPath(root, project, id)
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func readRaw(root, project, id string) ([]byte, error) {
	return os.ReadFile(rawPath(root, project, id))
}

// deleteMessages removes inbox messages (one id, or all of a project's when
// id is empty) and their raw files. It returns how many it removed.
func deleteMessages(ctx context.Context, p *platform.Platform, project, id string) (int, error) {
	q := `SELECT id FROM email_messages WHERE project = ? AND delivery = ?`
	args := []any{project, DeliveryInbox}
	if id != "" {
		q += ` AND id = ?`
		args = append(args, id)
	}
	rows, err := p.DB.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, x)
	}
	rows.Close()
	for _, x := range ids {
		if _, err := p.DB.SQL().ExecContext(ctx, `DELETE FROM email_messages WHERE id = ?`, x); err != nil {
			return 0, err
		}
		_ = os.Remove(rawPath(p.DataRoot, project, x))
	}
	return len(ids), nil
}

// ---- suppressions ----

// Suppression is an address the project will not send to.
type Suppression struct {
	Address   string    `json:"address"`
	Reason    string    `json:"reason" enum:"bounce,complaint,unsubscribe,manual" doc:"bounce: the relay rejected it permanently; complaint: marked as spam; unsubscribe: the person opted out; manual: added by hand"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func normAddr(a string) string { return strings.ToLower(strings.TrimSpace(a)) }

func addSuppression(ctx context.Context, db *sql.DB, project string, s Suppression) error {
	_, err := db.ExecContext(ctx, `INSERT INTO email_suppressions(project, address, reason, detail, created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(project, address) DO UPDATE SET reason = excluded.reason, detail = excluded.detail`,
		project, normAddr(s.Address), s.Reason, s.Detail, ts(time.Now()))
	return err
}

func listSuppressions(ctx context.Context, db *sql.DB, project string) ([]Suppression, error) {
	rows, err := db.QueryContext(ctx, `SELECT address, reason, detail, created_at FROM email_suppressions WHERE project = ? ORDER BY created_at DESC, address`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suppression{}
	for rows.Next() {
		var s Suppression
		var at string
		if err := rows.Scan(&s.Address, &s.Reason, &s.Detail, &at); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, s)
	}
	return out, rows.Err()
}

func deleteSuppression(ctx context.Context, db *sql.DB, project, address string) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM email_suppressions WHERE project = ? AND address = ?`, project, normAddr(address))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// suppressed returns which of addrs are suppressed for project.
func suppressed(ctx context.Context, db *sql.DB, project string, addrs []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, a := range addrs {
		var x string
		err := db.QueryRowContext(ctx, `SELECT address FROM email_suppressions WHERE project = ? AND address = ?`, project, normAddr(a)).Scan(&x)
		switch {
		case err == nil:
			out[normAddr(a)] = true
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	return out, nil
}

// reservedRecipient reports whether an address is at a domain reserved for
// examples and tests (RFC 2606, RFC 6761): no mail can ever reach it, so
// the box keeps such mail in the dev inbox instead of handing it to the
// relay, where it would only bounce and count against the sender.
func reservedRecipient(addr string) bool {
	a := normAddr(addr)
	if i := strings.LastIndex(a, "<"); i >= 0 {
		a = strings.TrimSuffix(a[i+1:], ">")
	}
	at := strings.LastIndex(a, "@")
	if at < 0 {
		return false
	}
	d := strings.TrimSuffix(a[at+1:], ".")
	switch d {
	case "example.com", "example.net", "example.org", "example", "test", "invalid", "localhost":
		return true
	}
	for _, s := range []string{".example.com", ".example.net", ".example.org", ".example", ".test", ".invalid", ".localhost"} {
		if strings.HasSuffix(d, s) {
			return true
		}
	}
	return false
}
