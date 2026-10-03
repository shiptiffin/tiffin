package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ProvisionReportPath is where `tiffin provision` records how each module went.
var ProvisionReportPath = "/var/lib/tiffin/platform/provision.json"

// ProvisionResult is one module's outcome.
type ProvisionResult struct {
	Module  string  `json:"module"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

// ProvisionReport is the last provisioning run.
type ProvisionReport struct {
	At      time.Time         `json:"at"`
	Results []ProvisionResult `json:"results"`
}

// Failed lists modules that failed.
func (r ProvisionReport) Failed() []string {
	var out []string
	for _, x := range r.Results {
		if x.Error != "" {
			out = append(out, x.Module)
		}
	}
	return out
}

// SaveProvisionReport writes the report for the status checks.
func SaveProvisionReport(r ProvisionReport) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(ProvisionReportPath, b, 0o644)
}

// provisionChecks turns the last report's failures into status checks.
func provisionChecks(context.Context) []Check {
	raw, err := os.ReadFile(ProvisionReportPath)
	if err != nil {
		return nil
	}
	var r ProvisionReport
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	failed := r.Failed()
	if len(failed) == 0 {
		return []Check{{Name: "provision", OK: true, Detail: fmt.Sprintf("%d modules installed", len(r.Results))}}
	}
	var details []string
	for _, x := range r.Results {
		if x.Error != "" {
			msg := x.Error
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			details = append(details, x.Module+": "+msg)
		}
	}
	return []Check{{Name: "provision", OK: false, Detail: "failed to install " + strings.Join(failed, ", ") + " — " + strings.Join(details, "; ") + ". Run `tiffin up` again after fixing."}}
}
