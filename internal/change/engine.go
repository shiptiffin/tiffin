package change

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
)

// Store persists project state and the change log. Commit must be atomic:
// it checks the project is still at c.Plan.BaseVersion, checks every op's
// precondition, applies the ops, appends the change, bumps the version and,
// if c.UndoOf is set, marks that change as undone — all or nothing.
type Store interface {
	// Load returns the project's current version (0 if it does not exist)
	// and its resources.
	Load(ctx context.Context, project string) (int64, map[string]Resource, error)
	// Commit atomically applies c. It sets c.Version. ErrConflict if the
	// version moved; *PreconditionError if an op no longer applies.
	Commit(ctx context.Context, c *Change) error
	// GetChange returns one change or ErrNotFound.
	GetChange(ctx context.Context, id string) (*Change, error)
	// ListChanges returns changes newest first. project "" means all.
	ListChanges(ctx context.Context, f ListFilter) ([]*Change, error)
	// ListProjects returns project names with at least one resource.
	ListProjects(ctx context.Context) ([]string, error)
}

// ListFilter narrows ListChanges.
type ListFilter struct {
	Project string
	Limit   int   // 0 means 50
	Before  int64 // only changes with a sequence number below this; 0 means no bound
}

// Authorizer decides whether a plan may be applied. It returns nil to allow
// or a reason string error to deny. Policy lives outside the engine.
type Authorizer func(p *Plan) error

// Engine plans and applies changes.
type Engine struct {
	Store Store
	Now   func() time.Time
	// Estimate, when set, measures what each irreversible op of a plan
	// would destroy (see AttachLosses). The box sets it; nil measures nothing.
	Estimate EstimateFunc
}

// NewEngine returns an engine over store.
func NewEngine(s Store) *Engine { return &Engine{Store: s, Now: time.Now} }

// Plan computes the ops that make project match desired. It never writes.
func (e *Engine) Plan(ctx context.Context, project string, desired map[string]Resource) (*Plan, error) {
	ver, current, err := e.Store.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	p := newPlan(project, ver, Diff(current, desired), "")
	AttachLosses(ctx, p, e.Estimate)
	return p, nil
}

// PlanUndo computes the plan that reverts change id. It fails with a
// *PreconditionError if anything the change touched was modified since.
func (e *Engine) PlanUndo(ctx context.Context, id string) (*Plan, error) {
	c, err := e.Store.GetChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.UndoneBy != "" {
		return nil, &PreconditionError{Address: id, Detail: "already undone by " + c.UndoneBy}
	}
	ver, current, err := e.Store.Load(ctx, c.Project)
	if err != nil {
		return nil, err
	}
	ops := make([]Op, len(c.Inverse))
	copy(ops, c.Inverse)
	if err := CheckPreconditions(current, ops); err != nil {
		var pe *PreconditionError
		if errors.As(err, &pe) {
			pe.Detail = "changed after " + id + " (" + pe.Detail + "); undo would overwrite newer work"
		}
		return nil, err
	}
	for i := range ops {
		ops[i].Risk, ops[i].Reason = Classify(ops[i])
	}
	p := newPlan(c.Project, ver, ops, id)
	AttachLosses(ctx, p, e.Estimate)
	return p, nil
}

// ApplyRequest is everything Apply needs.
type ApplyRequest struct {
	Plan      *Plan  // a fresh plan from Plan or PlanUndo
	Confirm   string // hash or unique prefix (≥8 chars) of the plan the caller reviewed
	Actor     Actor
	Intent    string
	Authorize Authorizer // nil allows everything
}

// Apply commits a plan once the caller has confirmed its hash and policy
// allows its risk. Without a matching confirmation it returns
// *ConfirmRequiredError carrying the plan. An empty plan is a no-op and
// returns (nil, nil).
func (e *Engine) Apply(ctx context.Context, r ApplyRequest) (*Change, error) {
	p := r.Plan
	if p.Empty() {
		return nil, nil
	}
	if !MatchHash(p.Hash, r.Confirm) {
		return nil, &ConfirmRequiredError{Plan: p, Mismatch: r.Confirm != ""}
	}
	if r.Authorize != nil {
		if err := r.Authorize(p); err != nil {
			return nil, &DeniedError{Plan: p, Reason: err.Error()}
		}
	}
	if strings.TrimSpace(r.Intent) == "" {
		r.Intent = p.Summary
	}
	c := &Change{
		ID:      ids.New("chg"),
		Project: p.Project,
		Actor:   r.Actor,
		Intent:  r.Intent,
		Plan:    *p,
		Inverse: Inverse(p.Ops),
		UndoOf:  p.UndoOf,
		At:      e.Now().UTC(),
	}
	if err := e.Store.Commit(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// MatchHash reports whether confirm identifies hash: the full hash or a
// prefix of at least 8 hex characters.
func MatchHash(hash, confirm string) bool {
	confirm = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(confirm)), "sha256:")
	return len(confirm) >= 8 && strings.HasPrefix(hash, confirm)
}

// Inverse returns the ops that undo ops, in reverse order.
func Inverse(ops []Op) []Op {
	inv := make([]Op, 0, len(ops))
	for i := len(ops) - 1; i >= 0; i-- {
		o := ops[i]
		r := Op{Address: o.Address, Before: o.After, After: o.Before, Fields: o.Fields}
		switch o.Action {
		case Create:
			r.Action = Delete
		case Delete:
			r.Action = Create
		default:
			r.Action = Update
		}
		r.Risk, r.Reason = Classify(r)
		inv = append(inv, r)
	}
	return inv
}

// CheckPreconditions verifies each op applies to current: creates need the
// address absent, updates and deletes need it present with spec == Before.
func CheckPreconditions(current map[string]Resource, ops []Op) error {
	for _, o := range ops {
		have, ok := current[o.Address]
		switch o.Action {
		case Create:
			if ok {
				return &PreconditionError{Address: o.Address, Detail: "already exists"}
			}
		case Update, Delete:
			if !ok {
				return &PreconditionError{Address: o.Address, Detail: "does not exist"}
			}
			if !jsonEqual(have.Spec, o.Before) {
				return &PreconditionError{Address: o.Address, Detail: "current settings differ from the plan's"}
			}
		default:
			return &PreconditionError{Address: o.Address, Detail: "unknown action " + string(o.Action)}
		}
	}
	return nil
}

// ApplyOps returns current with ops applied. It assumes preconditions hold.
func ApplyOps(current map[string]Resource, ops []Op) map[string]Resource {
	next := make(map[string]Resource, len(current))
	for k, v := range current {
		next[k] = v
	}
	for _, o := range ops {
		if o.Action == Delete {
			delete(next, o.Address)
		} else {
			next[o.Address] = Resource{Address: o.Address, Spec: o.After}
		}
	}
	return next
}

func newPlan(project string, base int64, ops []Op, undoOf string) *Plan {
	if ops == nil {
		ops = []Op{}
	}
	p := &Plan{Project: project, BaseVersion: base, Ops: ops, Risk: TierRead, UndoOf: undoOf}
	var nc, nu, nd int
	for _, o := range ops {
		p.Risk = MaxTier(p.Risk, o.Risk)
		switch o.Action {
		case Create:
			nc++
		case Update:
			nu++
		case Delete:
			nd++
		}
	}
	if len(ops) == 0 {
		p.Summary = "no changes"
	} else {
		p.Summary = fmt.Sprintf("%d to create, %d to update, %d to delete (%s)", nc, nu, nd, p.Risk)
	}
	p.Hash = hashPlan(p)
	return p
}

// hashPlan binds a confirmation to the project, base version, undo target
// and exact ops. Risk and reasons are derived, so they are left out.
func hashPlan(p *Plan) string {
	type hop struct {
		Action  Action          `json:"a"`
		Address string          `json:"k"`
		Before  json.RawMessage `json:"b,omitempty"`
		After   json.RawMessage `json:"f,omitempty"`
	}
	h := struct {
		V       int    `json:"v"`
		Project string `json:"p"`
		Base    int64  `json:"base"`
		UndoOf  string `json:"u,omitempty"`
		Ops     []hop  `json:"ops"`
	}{V: 1, Project: p.Project, Base: p.BaseVersion, UndoOf: p.UndoOf, Ops: make([]hop, len(p.Ops))}
	for i, o := range p.Ops {
		h.Ops[i] = hop{o.Action, o.Address, canon(o.Before), canon(o.After)}
	}
	b, _ := compact(h)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func canon(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(r, &v) != nil {
		return r
	}
	c, _ := compact(v)
	return c
}
