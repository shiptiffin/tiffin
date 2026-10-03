// Package approvals lets an agent ask a human to approve a plan it is not
// allowed to apply on its own (an irreversible or outbound change), and lets
// the human approve it with a passkey.
//
// The approval is bound to the exact plan: its hash, project and base
// version. It is single-use, expires after a day, and only the agent that
// asked can spend it. The passkey assertion's challenge is issued for one
// approval and stored with it, so a signature cannot be replayed for another.
package approvals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Status values.
const (
	Pending  = "pending"
	Approved = "approved"
	Rejected = "rejected"
	Used     = "used"
	Expired  = "expired"
)

// TTL is how long a request waits for a human.
const TTL = 24 * time.Hour

// Errors.
var (
	ErrNotFound   = errors.New("approval not found")
	ErrNotPending = errors.New("approval is no longer pending")
	ErrNoPasskey  = errors.New("no passkey registered: add one in the dashboard (Settings → Passkeys) first")
	ErrInvalid    = errors.New("approval does not match this plan or caller")
	ErrHumanOnly  = errors.New("only a human with a dashboard session can approve")
)

// Approval is one request.
type Approval struct {
	ID          string       `json:"id"`
	Project     string       `json:"project"`
	PlanHash    string       `json:"planHash"`
	Plan        *change.Plan `json:"plan"`
	Intent      string       `json:"intent"`
	RequestedBy string       `json:"requestedBy" doc:"token ID of the agent that asked"`
	Requester   string       `json:"requester" doc:"name of the agent (and session)"`
	Status      string       `json:"status" enum:"pending,approved,rejected,used,expired"`
	CreatedAt   time.Time    `json:"createdAt"`
	ExpiresAt   time.Time    `json:"expiresAt"`
	DecidedAt   *time.Time   `json:"decidedAt,omitempty"`
	DecidedBy   string       `json:"decidedBy,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	UsedBy      string       `json:"usedBy,omitempty" doc:"the change that spent it"`
}

// Manager stores approvals and passkeys.
type Manager struct {
	db  *state.DB
	wa  *webauthn.WebAuthn
	now func() time.Time
}

// New returns a manager. rpID is the dashboard host (e.g.
// "dashboard.tiffin.localhost"); origin its URL.
func New(db *state.DB, rpID, origin string) (*Manager, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "Tiffin",
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, err
	}
	return &Manager{db: db, wa: wa, now: time.Now}, nil
}

// Request records (or returns the existing pending) approval for plan by p.
func (m *Manager) Request(ctx context.Context, p *tokens.Principal, plan *change.Plan, intent string) (*Approval, error) {
	if a, err := m.find(ctx, `WHERE plan_hash = ? AND requested_by = ? AND status = ? AND expires_at > ?`,
		plan.Hash, p.TokenID, Pending, ts(m.now())); err == nil {
		return a, nil
	}
	body, _ := json.Marshal(plan)
	now := m.now().UTC()
	who := p.Name
	if p.Session != "" {
		who += " (" + p.Session + ")"
	}
	a := &Approval{ID: ids.New("apr"), Project: plan.Project, PlanHash: plan.Hash, Plan: plan, Intent: intent,
		RequestedBy: p.TokenID, Requester: who, Status: Pending, CreatedAt: now, ExpiresAt: now.Add(TTL)}
	_, err := m.db.SQL().ExecContext(ctx, `INSERT INTO approvals(id, project, plan_hash, plan, intent, requested_by, requester, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.ID, a.Project, a.PlanHash, string(body), a.Intent, a.RequestedBy, a.Requester, a.Status, ts(a.CreatedAt), ts(a.ExpiresAt))
	if err != nil {
		return nil, err
	}
	_ = m.db.Audit(ctx, p.TokenID, "approval.request", a.ID, map[string]any{"project": a.Project, "risk": plan.Risk, "plan": plan.Hash[:12]})
	return a, nil
}

// Get returns one approval (marking it expired if its time passed).
func (m *Manager) Get(ctx context.Context, id string) (*Approval, error) {
	return m.find(ctx, `WHERE id = ?`, id)
}

// List returns approvals, newest first, optionally by status.
func (m *Manager) List(ctx context.Context, status string, limit int) ([]*Approval, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := approvalCols + ` FROM approvals`
	args := []any{}
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	rows, err := m.db.SQL().QueryContext(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Approval{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		m.expire(a)
		if status == "" || a.Status == status {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

// Spend checks that approval id approves exactly plan for p, and marks it
// used by changeID. Call it right before committing the change.
func (m *Manager) Spend(ctx context.Context, id string, p *tokens.Principal, plan *change.Plan) error {
	a, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	if a.Status != Approved {
		return fmt.Errorf("%w: approval %s is %s", ErrInvalid, id, a.Status)
	}
	if a.PlanHash != plan.Hash || a.Project != plan.Project || a.RequestedBy != p.TokenID {
		return fmt.Errorf("%w: it was granted for plan %s by %s", ErrInvalid, a.PlanHash[:12], a.Requester)
	}
	res, err := m.db.SQL().ExecContext(ctx, `UPDATE approvals SET status = ? WHERE id = ? AND status = ?`, Used, id, Approved)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: already used", ErrInvalid)
	}
	return nil
}

// MarkUsedBy records the change an approval paid for.
func (m *Manager) MarkUsedBy(ctx context.Context, id, changeID string) {
	_, _ = m.db.SQL().ExecContext(ctx, `UPDATE approvals SET used_by = ? WHERE id = ?`, changeID, id)
}

// Reject declines a pending approval.
func (m *Manager) Reject(ctx context.Context, id string, by *tokens.Principal, reason string) (*Approval, error) {
	if err := humanOnly(by); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	res, err := m.db.SQL().ExecContext(ctx, `UPDATE approvals SET status = ?, decided_at = ?, decided_by = ?, reason = ?
		WHERE id = ? AND status = ? AND expires_at > ?`, Rejected, ts(now), by.TokenID, reason, id, Pending, ts(now))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotPending
	}
	_ = m.db.Audit(ctx, by.TokenID, "approval.reject", id, map[string]any{"reason": reason})
	return m.Get(ctx, id)
}

// humanOnly: approvals come from people (the owner or a dashboard session),
// never from agent tokens, whatever their scopes.
func humanOnly(p *tokens.Principal) error {
	if p == nil || p.Kind == tokens.KindAgent || !p.BoxAdmin() {
		return ErrHumanOnly
	}
	return nil
}

func (m *Manager) expire(a *Approval) {
	if (a.Status == Pending || a.Status == Approved) && !m.now().Before(a.ExpiresAt) {
		a.Status = Expired
		_, _ = m.db.SQL().Exec(`UPDATE approvals SET status = ? WHERE id = ?`, Expired, a.ID)
	}
}

const approvalCols = `SELECT id, project, plan_hash, plan, intent, requested_by, requester, status, created_at, expires_at, decided_at, decided_by, reason, used_by`

func (m *Manager) find(ctx context.Context, where string, args ...any) (*Approval, error) {
	a, err := scan(m.db.SQL().QueryRowContext(ctx, approvalCols+` FROM approvals `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.expire(a)
	return a, nil
}

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (*Approval, error) {
	var a Approval
	var plan, created, expires string
	var decidedAt, decidedBy, reason, usedBy sql.NullString
	if err := r.Scan(&a.ID, &a.Project, &a.PlanHash, &plan, &a.Intent, &a.RequestedBy, &a.Requester, &a.Status,
		&created, &expires, &decidedAt, &decidedBy, &reason, &usedBy); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(plan), &a.Plan)
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if decidedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, decidedAt.String)
		a.DecidedAt = &t
	}
	a.DecidedBy, a.Reason, a.UsedBy = decidedBy.String, reason.String, usedBy.String
	return &a, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
