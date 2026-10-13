package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
)

// A plan that would undo changes made outside the config says so on the
// op, in red at the end, and offers tiffin pull before --discard-drift.
func TestRenderDrift(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	p := &change.Plan{Project: "reelbird", BaseVersion: 7, Hash: strings.Repeat("ab", 32), Summary: "0 to create, 0 to update, 1 to delete (reversible)",
		Ops: []change.Op{{Action: change.Delete, Address: "domain/reelbird.app", Risk: change.TierReversible, Reason: "removes the domain",
			Drift: &change.Drift{Change: "chg_1", By: "claude-code", At: at, Via: "tiffin domains add"}}}}
	p.Warnings = []string{change.DriftWarning(p)}
	yes := true
	for _, tc := range []struct {
		status int
		body   any
	}{
		{200, p},
		{428, api.Problem{Status: 428, Code: "confirm_required", Plan: p}},
		{428, api.Problem{Status: 428, Code: "drift", Detail: p.Warnings[0], Plan: p}},
	} {
		var out bytes.Buffer
		a := &app{io: IO{Out: &out, Err: &out, TTY: &yes, Env: func(string) string { return "1" }}}
		raw, _ := json.Marshal(tc.body)
		a.emit(tc.status, raw)
		s := out.String()
		for _, want := range []string{
			"! not in your config: added 2026-10-13 by claude-code with `tiffin domains add` (chg_1)",
			"! drift: Your tiffin.config.ts doesn't have changes made outside it: domain/reelbird.app (added 2026-10-13",
			"To keep those changes: tiffin pull, then plan again.",
			"To remove them: tiffin apply --confirm abababababab --discard-drift",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%d %T: missing %q in:\n%s", tc.status, tc.body, want, s)
			}
		}
		if strings.Count(s, "Your tiffin.config.ts") != 1 || strings.Contains(s, "error:") {
			t.Errorf("%d: the warning once, no error line:\n%s", tc.status, s)
		}
	}
}
