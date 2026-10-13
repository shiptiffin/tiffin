package change

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Where a change came in (Change.Via). Any other operation records its CLI
// words, e.g. "tiffin domains add".
const (
	// ViaConfig is a whole manifest applied from a config file
	// (tiffin.config.ts): `tiffin apply`, or the apply API by default.
	ViaConfig = "config"
	// ViaDashboard is anything done in the dashboard.
	ViaDashboard = "dashboard"
	// ViaAPI is an edited manifest (not a config file) sent with a key.
	ViaAPI = "api"
	// ViaSystem is the box itself (disk guard, a verified sender...).
	ViaSystem = "system"
)

type viaKey struct{}

// WithVia records on ctx the path a request came in by; Apply stores it on
// the change unless the request names its own (ApplyRequest.Via).
func WithVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey{}, via)
}

// ViaFrom returns the path WithVia recorded, or "".
func ViaFrom(ctx context.Context) string {
	v, _ := ctx.Value(viaKey{}).(string)
	return v
}

// Drift is a change made outside the config file that an op of a config
// plan would delete or revert.
type Drift struct {
	Change string    `json:"change" doc:"The change that made what this op would delete or revert"`
	By     string    `json:"by" doc:"Who made it (a person, an agent's key name, or the box)"`
	At     time.Time `json:"at"`
	Via    string    `json:"via,omitempty" doc:"How it was made: dashboard, api, system, or the command (tiffin domains add)"`
	// Fields are the top-level fields an update would put back ("" for a
	// whole value, such as an env var). Empty for a delete.
	Fields []string `json:"fields,omitempty"`
}

// HasDrift reports whether any op of p deletes or reverts a change made
// outside the config.
func (p *Plan) HasDrift() bool {
	for _, o := range p.Ops {
		if o.Drift != nil {
			return true
		}
	}
	return false
}

// driftScan bounds how far back MarkDrift reads the change log: the newest
// 100 changes answer nearly every plan, and a long history is read once.
var driftScan = []int{100, 1000}

// MarkDrift marks the ops of a plan made from a config file that would
// delete or revert something last set outside one: by the dashboard, an
// operation such as `tiffin domains add`, an edited manifest sent through
// the API, an undo or the box itself. It reads the project's change log:
//
//   - a delete is drift when the resource was created outside the config
//     and no config apply has touched it since (the file never had it);
//   - an update is drift for each field it would set back to a value it
//     had before changes made outside the config since the last config
//     apply that set that field (a stale file holds the old value).
//
// Creates are never drift. It adds the plan's drift warning (first) and
// changes nothing else: the hash is the same.
func (e *Engine) MarkDrift(ctx context.Context, p *Plan) error {
	var want []int
	for i, o := range p.Ops {
		if o.Action != Create && !Unmanaged(o.Address) {
			want = append(want, i)
		}
	}
	if len(want) == 0 {
		return nil
	}
	for n, limit := range driftScan {
		hist, err := e.Store.ListChanges(ctx, ListFilter{Project: p.Project, Limit: limit})
		if err != nil {
			return err
		}
		for _, i := range want {
			p.Ops[i].Drift = driftOf(p.Ops[i], hist)
		}
		if len(hist) < limit || n == len(driftScan)-1 {
			break
		}
		// Unresolved ops (no config apply found yet) may need older changes.
		if !unresolved(p, want, hist) {
			break
		}
	}
	if w := DriftWarning(p); w != "" {
		p.Warnings = append([]string{w}, p.Warnings...)
	}
	return nil
}

// unresolved reports whether some op's history reached the end of hist
// without finding where its resource or fields came from.
func unresolved(p *Plan, want []int, hist []*Change) bool {
	for _, i := range want {
		if p.Ops[i].Drift == nil && !settled(p.Ops[i], hist) {
			return true
		}
	}
	return false
}

// settled reports whether hist holds a config apply or the creation of
// the op's resource, so older changes cannot matter.
func settled(o Op, hist []*Change) bool {
	for _, c := range hist {
		if h := opOn(c, o.Address); h != nil && (c.Via == ViaConfig || h.Action == Create) {
			return true
		}
	}
	return false
}

// driftOf returns the drift of one op of a config plan, given the
// project's changes newest first, or nil.
func driftOf(o Op, hist []*Change) *Drift {
	switch o.Action {
	case Delete:
		for _, c := range hist {
			h := opOn(c, o.Address)
			if h == nil {
				continue
			}
			if c.Via == ViaConfig {
				return nil // a config apply has had it
			}
			if h.Action == Create {
				return newDrift(c, nil)
			}
		}
	case Update:
		fields := o.Fields
		if len(fields) == 0 {
			fields = []string{""}
		}
		var by *Change
		var reverted []string
		for _, f := range fields {
			target := fieldOf(o.After, f)
			for _, c := range hist {
				h := opOn(c, o.Address)
				if h == nil || bytes.Equal(fieldOf(h.Before, f), fieldOf(h.After, f)) {
					continue // didn't touch this field
				}
				if c.Via == ViaConfig {
					break
				}
				if bytes.Equal(fieldOf(h.Before, f), target) {
					reverted = append(reverted, f)
					if by == nil {
						by = newest(hist, o.Address, f)
					}
					break
				}
				if h.Action == Create {
					break
				}
			}
		}
		if by != nil {
			return newDrift(by, reverted)
		}
	}
	return nil
}

// newest returns the newest change in hist that set field f of address.
func newest(hist []*Change, address, f string) *Change {
	for _, c := range hist {
		if h := opOn(c, address); h != nil && !bytes.Equal(fieldOf(h.Before, f), fieldOf(h.After, f)) {
			return c
		}
	}
	return nil
}

func newDrift(c *Change, fields []string) *Drift {
	by := c.Actor.Name
	if by == "" {
		by = c.Actor.ID
	}
	return &Drift{Change: c.ID, By: by, At: c.At, Via: c.Via, Fields: fields}
}

// opOn returns c's op on address, or nil.
func opOn(c *Change, address string) *Op {
	for i := range c.Plan.Ops {
		if c.Plan.Ops[i].Address == address {
			return &c.Plan.Ops[i]
		}
	}
	return nil
}

// fieldOf returns field f of a spec in canonical JSON (nil when absent);
// f == "" is the whole spec.
func fieldOf(spec json.RawMessage, f string) []byte {
	if len(spec) == 0 {
		return nil
	}
	if f == "" {
		return canon(spec)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(spec, &m) != nil {
		return nil
	}
	v, ok := m[f]
	if !ok {
		return nil
	}
	return canon(v)
}

// DriftWarning is the plan warning that lists what its config file lacks,
// or "" when nothing drifted.
func DriftWarning(p *Plan) string {
	var items []string
	for _, o := range p.Ops {
		if o.Drift == nil {
			continue
		}
		what, verb := o.Address, "changed"
		if o.Action == Delete {
			verb = "added"
		} else if f := strings.Join(nonEmpty(o.Drift.Fields), ", "); f != "" {
			what += " " + f
		}
		items = append(items, fmt.Sprintf("%s (%s %s)", what, verb, DescribeDrift(o.Drift)))
	}
	if len(items) == 0 {
		return ""
	}
	return "Your tiffin.config.ts doesn't have changes made outside it: " + strings.Join(items, "; ") +
		". Run `tiffin pull` to bring them into your config, or confirm with --discard-drift (API: discardDrift: true) to remove them."
}

// DescribeDrift says when, by whom and how, in plain words:
// "2026-10-13 by claude-code with `tiffin domains add`".
func DescribeDrift(d *Drift) string {
	s := d.At.UTC().Format("2006-01-02") + " by " + d.By
	switch {
	case d.Via == ViaDashboard:
		s += " in the dashboard"
	case d.Via == ViaAPI:
		s += " through the API"
	case strings.HasPrefix(d.Via, "tiffin "):
		s += " with `" + d.Via + "`"
	}
	return s
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
