package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// loadProvisionReport reads the last report (false: none).
func loadProvisionReport() (ProvisionReport, bool) {
	var r ProvisionReport
	raw, err := os.ReadFile(ProvisionReportPath)
	if err != nil || json.Unmarshal(raw, &r) != nil {
		return r, false
	}
	return r, true
}

// A module whose provisioning failed for a moment (apt's index rewritten
// under it, a download cut off) would stay failed until the next `tiffin
// up`. The box runs `tiffin provision` again instead, in a unit of its own
// (it may restart services, this one's helpers included), backing off
// while it keeps failing.
var (
	provisionRetryFirst = 2 * time.Minute
	provisionRetryMax   = 6 * time.Hour
	launchProvision     = func(ctx context.Context) error {
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		out, err := exec.CommandContext(ctx, "systemd-run", "--unit", "tiffin-provision-retry", "--collect", "--quiet", "--wait", bin, "provision").CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

// retryProvision provisions again until the report has no failures.
func (p *Platform) retryProvision(ctx context.Context) {
	wait := provisionRetryFirst
	for {
		r, ok := loadProvisionReport()
		if !ok || len(r.Failed()) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		p.Log.Info("provisioning again", "failed", strings.Join(r.Failed(), ", "))
		if err := launchProvision(ctx); err != nil {
			p.Log.Warn("provisioning again", "err", err)
		}
		wait = min(2*wait, provisionRetryMax)
	}
}

// provisionChecks turns the last report's failures into status checks.
func provisionChecks(context.Context) []Check {
	r, ok := loadProvisionReport()
	if !ok {
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

// Needer lets a provisioner wait for others (by module name) to finish
// first, e.g. backup needs postgres installed. Every provisioner implicitly
// needs "base".
type Needer interface{ Needs() []string }

// ProvisionAll runs every Provisioner concurrently, each starting once the
// modules it needs are done. Failures are recorded, never fatal: one broken
// service must not block the rest. When every one succeeded, the downloads
// they installed from are removed.
func ProvisionAll(ctx context.Context, s *System, log func(string)) ProvisionReport {
	type node struct {
		m    Module
		pv   Provisioner
		done chan struct{}
		err  error
	}
	nodes := map[string]*node{}
	var order []string
	for _, m := range Modules() {
		if pv, ok := m.(Provisioner); ok {
			nodes[m.Name()] = &node{m: m, pv: pv, done: make(chan struct{})}
			order = append(order, m.Name())
		}
	}
	results := make([]ProvisionResult, len(order))
	var wg sync.WaitGroup
	for i, name := range order {
		n := nodes[name]
		needs := []string{}
		if name != "base" {
			needs = append(needs, "base")
		}
		if nd, ok := n.m.(Needer); ok {
			needs = append(needs, nd.Needs()...)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(n.done)
			for _, dep := range needs {
				if d, ok := nodes[dep]; ok && d != n {
					<-d.done
					if d.err != nil && dep == "base" {
						n.err = fmt.Errorf("skipped: base failed")
						results[i] = ProvisionResult{Module: name, Error: n.err.Error()}
						return
					}
				}
			}
			start := time.Now()
			n.err = n.pv.Provision(ctx, s)
			r := ProvisionResult{Module: name, Seconds: time.Since(start).Seconds()}
			if n.err != nil {
				r.Error = n.err.Error()
				log(fmt.Sprintf("%s FAILED after %s: %v", name, time.Since(start).Round(time.Second), n.err))
			} else {
				log(fmt.Sprintf("%s ready (%s)", name, time.Since(start).Round(time.Millisecond)))
			}
			results[i] = r
		}()
	}
	wg.Wait()
	report := ProvisionReport{At: time.Now().UTC(), Results: results}
	if len(report.Failed()) == 0 {
		s.ClearDownloads() // a failure keeps them for the retry
	}
	return report
}
