package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
)

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(io.LimitReader(r, 16<<20)) }

// emit prints an API response: raw JSON for machines, a readable view for people.
func (a *app) emit(status int, raw []byte) {
	if !a.tty() {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = []byte(`{"ok":true}`)
		}
		var v any
		if json.Unmarshal(raw, &v) == nil {
			writeJSON(a.io.Out, v)
		} else {
			a.io.Out.Write(raw)
		}
		return
	}
	out := a.io.Out
	if status >= 300 {
		var p api.Problem
		_ = json.Unmarshal(raw, &p)
		if p.Plan != nil && (p.Code == "confirm_required" || p.Code == "plan_mismatch" || p.Code == "forbidden") {
			renderPlan(out, p.Plan, a.color())
		}
		w := a.io.Err
		if p.Code == "confirm_required" {
			w = out
		} else {
			fmt.Fprintf(w, "%s %s\n", a.paint("error:", red), orDefault(p.Detail, p.Title))
		}
		for _, f := range p.Errors {
			fmt.Fprintf(w, "  %s  %s\n", orDefault(f.Path, "-"), f.Message)
		}
		if p.Hint != "" && p.Code != "confirm_required" && p.Code != "plan_mismatch" {
			fmt.Fprintf(w, "%s %s\n", a.paint("next:", dim), p.Hint)
		}
		if p.Code == "confirm_required" || p.Code == "plan_mismatch" {
			fmt.Fprintf(out, "%s nothing has changed yet. Re-run with %s\n", a.paint("→", amber), a.paint("--confirm "+p.Plan.Hash[:12], bold))
		}
		return
	}
	// Recognise the shapes worth rendering specially.
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) == nil {
		if _, ok := probe["ops"]; ok {
			var p change.Plan
			if json.Unmarshal(raw, &p) == nil {
				renderPlan(out, &p, a.color())
				if !p.Empty() {
					fmt.Fprintf(out, "%s apply with %s\n", a.paint("→", amber), a.paint("tiffin apply --confirm "+p.Hash[:12], bold))
				}
				return
			}
		}
		if _, ok := probe["applied"]; ok {
			var r api.ApplyResult
			if json.Unmarshal(raw, &r) == nil {
				if !r.Applied {
					fmt.Fprintln(out, "No changes. The box already matches.")
					return
				}
				c := r.Change
				fmt.Fprintf(out, "%s %s  %s (project %s, version %d)\n", a.paint("✓ applied", green), c.ID, c.Plan.Summary, c.Project, c.Version)
				fmt.Fprintf(out, "%s undo with %s\n", a.paint("→", amber), a.paint("tiffin undo "+c.ID, bold))
				return
			}
		}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		fmt.Fprintln(out, "Done.")
		return
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		writeJSON(out, v)
	} else {
		out.Write(raw)
	}
}

const (
	red   = "31"
	green = "32"
	amber = "33"
	dim   = "2"
	bold  = "1"
)

func (a *app) color() bool { return a.tty() && a.io.Env("NO_COLOR") == "" }

func (a *app) paint(s, code string) string { return paint(a.color(), s, code) }

func paint(on bool, s, code string) string {
	if !on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func renderPlan(w io.Writer, p *change.Plan, color bool) {
	defer func() {
		for _, warn := range p.Warnings {
			fmt.Fprintf(w, "%s %s\n", paint(color, "! warning:", amber), warn)
		}
	}()
	if p.Empty() {
		fmt.Fprintf(w, "No changes for %s. The box already matches.\n", p.Project)
		return
	}
	title := "Plan for " + p.Project
	if p.UndoOf != "" {
		title = "Undo plan for " + p.UndoOf
	}
	fmt.Fprintf(w, "%s  %s  %s\n", paint(color, title, bold), riskBadge(p.Risk, color), paint(color, fmt.Sprintf("base v%d · %s", p.BaseVersion, p.Hash[:12]), dim))
	width := 0
	for _, o := range p.Ops {
		width = max(width, len(o.Address))
	}
	for _, o := range p.Ops {
		sym, code := "~", amber
		switch o.Action {
		case change.Create:
			sym, code = "+", green
		case change.Delete:
			sym, code = "-", red
		}
		detail := o.Reason
		if len(o.Fields) > 0 {
			detail = strings.Join(o.Fields, ", ") + " · " + detail
		}
		fmt.Fprintf(w, "  %s %-*s  %s %s\n", paint(color, sym, code), width, o.Address, riskBadgePadded(o.Risk, 12, color), paint(color, detail, dim))
	}
	fmt.Fprintf(w, "%s\n", p.Summary)
}

// riskBadgePadded pads before colouring so escape codes don't break alignment.
func riskBadgePadded(t change.Tier, width int, color bool) string {
	pad := strings.Repeat(" ", max(0, width-len(t)))
	return riskBadge(t, color) + pad
}

func riskBadge(t change.Tier, color bool) string {
	code := dim
	switch t {
	case change.TierOutbound:
		code = amber
	case change.TierIrreversible:
		code = red
	}
	return paint(color, string(t), code)
}
