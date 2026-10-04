// Package change is Tiffin's core primitive. Every mutation of a project —
// by a human, an agent or the system — is a Change: who did it, why, a typed
// diff of resources, a risk tier, the inverse needed to undo it, and a plan
// hash that binds a confirmation to exactly what was reviewed.
//
// The flow is always plan → review → apply. Plans are pure and cheap;
// apply re-plans, checks the confirmed hash, checks policy, and commits with
// optimistic concurrency against the project's version.
package change

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Tier is how risky an operation is. Tiers map to the scope a token needs
// to apply a plan (an API key with full access holds them all) and are shown
// with every plan, so people and agents see what is irreversible.
type Tier string

const (
	TierRead         Tier = "read"
	TierReversible   Tier = "reversible"
	TierOutbound     Tier = "outbound"
	TierIrreversible Tier = "irreversible"
)

// Rank orders tiers from least to most risky.
func (t Tier) Rank() int {
	switch t {
	case TierRead:
		return 0
	case TierReversible:
		return 1
	case TierOutbound:
		return 2
	case TierIrreversible:
		return 3
	}
	return 4 // unknown tiers are treated as the most dangerous
}

// MaxTier returns the riskier of two tiers.
func MaxTier(a, b Tier) Tier {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// Action is what an operation does to one resource.
type Action string

const (
	Create Action = "create"
	Update Action = "update"
	Delete Action = "delete"
)

// Resource is one addressable piece of desired state inside a project,
// e.g. "app/web", "service/postgres", "bucket/media", "env/LOG_LEVEL".
type Resource struct {
	Address string          `json:"address"`
	Spec    json.RawMessage `json:"spec"` // compact canonical JSON
}

// Op is one step of a plan.
type Op struct {
	Action  Action          `json:"action"`
	Address string          `json:"address"`
	Before  json.RawMessage `json:"before,omitempty"` // nil for create
	After   json.RawMessage `json:"after,omitempty"`  // nil for delete
	Fields  []string        `json:"fields,omitempty"` // top-level fields changed (update only)
	Risk    Tier            `json:"risk"`
	Reason  string          `json:"reason"` // why this op has this tier, in plain words
	// Loss is what an irreversible op would destroy, measured when the plan
	// was made (absent when the box couldn't measure it). Not hashed.
	Loss *Loss `json:"loss,omitempty"`
}

// Actor is who asked for a change.
type Actor struct {
	Kind    string `json:"kind"` // "human", "agent" or "system"
	ID      string `json:"id"`   // token ID, user ID or "system"
	Name    string `json:"name,omitempty"`
	Session string `json:"session,omitempty"` // agent session, for the activity timeline
	Model   string `json:"model,omitempty"`   // the model an agent says it runs, self-reported
}

// Plan is a proposed, not-yet-applied set of ops against a known version.
type Plan struct {
	Project     string `json:"project"`
	BaseVersion int64  `json:"baseVersion"`
	Ops         []Op   `json:"ops"`
	Risk        Tier   `json:"risk"`
	Hash        string `json:"hash"`
	Summary     string `json:"summary"`
	UndoOf      string `json:"undoOf,omitempty"`
	// Warnings are things the manifest probably did not mean (auth without
	// email, env that replaces what the box sets). Not part of the hash.
	Warnings []string `json:"warnings,omitempty"`
}

// Empty reports whether the plan changes nothing.
func (p *Plan) Empty() bool { return len(p.Ops) == 0 }

// Change is an applied plan: the audit record and the unit of undo.
type Change struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
	Version  int64     `json:"version"` // project version after this change
	Actor    Actor     `json:"actor"`
	Intent   string    `json:"intent"`
	Plan     Plan      `json:"plan"`
	Inverse  []Op      `json:"inverse"`
	UndoOf   string    `json:"undoOf,omitempty"`
	UndoneBy string    `json:"undoneBy,omitempty"`
	At       time.Time `json:"at"`
}

// Errors. Callers match with errors.Is / errors.As.
var (
	ErrNotFound = errors.New("not found")
	// ErrConflict means the project moved on since the plan was made.
	ErrConflict = errors.New("project changed since plan; re-plan and confirm again")
)

// ConfirmRequiredError is returned by Apply when no confirmation was given
// or the given hash does not match a fresh plan. It carries the fresh plan
// so the caller can show it and retry with --confirm <plan.Hash>.
type ConfirmRequiredError struct {
	Plan     *Plan
	Mismatch bool // a hash was given but did not match
}

func (e *ConfirmRequiredError) Error() string {
	if e.Mismatch {
		return fmt.Sprintf("plan changed: confirm hash does not match; review the new plan and confirm %s", e.Plan.Hash[:12])
	}
	return fmt.Sprintf("confirmation required: %s; apply with --confirm %s", e.Plan.Summary, e.Plan.Hash[:12])
}

// DeniedError is returned when policy refuses a plan's risk tier.
type DeniedError struct {
	Plan   *Plan
	Reason string
}

func (e *DeniedError) Error() string { return "denied: " + e.Reason }

// PreconditionError means an op no longer applies to current state
// (e.g. undoing a change whose resources were modified afterwards).
type PreconditionError struct {
	Address string
	Detail  string
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("%s: %s", e.Address, e.Detail)
}
