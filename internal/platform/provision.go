package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// Needer lets a provisioner wait for others (by module name) to finish
// first, e.g. backup needs postgres installed. Every provisioner implicitly
// needs "base".
type Needer interface{ Needs() []string }

// ProvisionAll runs every Provisioner concurrently, each starting once the
// modules it needs are done. Failures are recorded, never fatal: one broken
// service must not block the rest.
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
	return ProvisionReport{At: time.Now().UTC(), Results: results}
}
