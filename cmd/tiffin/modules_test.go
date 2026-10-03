package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/cli"
	"github.com/btahir/tiffin/internal/mcp"
)

// Every module's operations must coexist: unique IDs, paths, CLI words and
// schema names. This builds the full API and MCP tool list like the binary does.
func TestAllModulesRegister(t *testing.T) {
	a := api.New(api.Deps{})
	seen := map[string]string{}
	hidden := 0
	for _, o := range a.Operations() {
		if cp := api.CLIPath(o); len(cp) == 1 && cp[0] == "-" {
			hidden++ // dashboard-only
			continue
		}
		cli := ""
		for _, w := range api.CLIPath(o) {
			cli += w + " "
		}
		if prev, ok := seen[cli]; ok {
			t.Errorf("CLI path %q used by %s and %s", cli, prev, o.OperationID)
		}
		seen[cli] = o.OperationID
		if o.Summary == "" || o.Description == "" {
			t.Errorf("%s: missing summary/description", o.OperationID)
		}
	}
	if tools := mcp.Tools(a); len(tools) != len(a.Operations())-hidden {
		t.Fatalf("%d tools for %d operations", len(tools), len(a.Operations()))
	}
	// Adding or removing an operation is a deliberate API change: update this.
	const wantOps = 164
	if n := len(a.Operations()); n != wantOps {
		t.Errorf("%d operations, want %d", n, wantOps)
	}
}

// The operations behind creating projects and apps from the dashboard.
func TestCreateOperations(t *testing.T) {
	a := api.New(api.Deps{})
	want := map[string]struct {
		method, path, cli, risk string
		outbound                bool
	}{
		"project-manifest": {"GET", "/v1/projects/{project}/manifest", "projects manifest", api.RiskRead, false},
		"templates-list":   {"GET", "/v1/templates", "templates list", api.RiskRead, false},
		"deploy-template":  {"POST", "/v1/projects/{project}/apps/{app}/deploys/template", "deploys template", api.RiskWrite, false},
		"deploy-git":       {"POST", "/v1/projects/{project}/apps/{app}/deploys/git", "deploys git", api.RiskWrite, true},
		"box-resources":    {"GET", "/v1/box/resources", "box resources", api.RiskRead, false},
	}
	for _, o := range a.Operations() {
		w, ok := want[o.OperationID]
		if !ok {
			continue
		}
		delete(want, o.OperationID)
		if o.Method != w.method || o.Path != w.path || strings.Join(api.CLIPath(o), " ") != w.cli || api.RiskOf(o) != w.risk || api.IsOutbound(o) != w.outbound {
			t.Errorf("%s: %s %s cli=%v risk=%s outbound=%v", o.OperationID, o.Method, o.Path, api.CLIPath(o), api.RiskOf(o), api.IsOutbound(o))
		}
	}
	if len(want) > 0 {
		t.Errorf("missing operations: %v", want)
	}
}

// A body field named like a global flag (deploys git's url) becomes
// --<command>-<name>, so the global --url still points the CLI at a box.
func TestBodyFlagNamedLikeGlobal(t *testing.T) {
	var out, errb bytes.Buffer
	no := false
	cli.Execute(context.Background(), []string{"deploys", "git", "--help"}, cli.IO{Out: &out, Err: &errb, TTY: &no,
		Env: func(string) string { return "" }})
	help := out.String()
	if !strings.Contains(help, "--git-url string") || !strings.Contains(help, "--url string       box API URL") {
		t.Fatalf("deploys git --help:\n%s%s", help, errb.String())
	}
}
