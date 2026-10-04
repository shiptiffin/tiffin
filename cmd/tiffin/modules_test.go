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
	const wantOps = 181
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
		"project-usage":    {"GET", "/v1/projects/{project}/usage", "projects usage", api.RiskRead, false},
		"box-settings-get": {"GET", "/v1/box/settings", "box settings get", api.RiskRead, false},
		"box-settings-set": {"PUT", "/v1/box/settings", "box settings set", api.RiskWrite, false},
		// Deploying from GitHub.
		"github-status":     {"GET", "/v1/github", "github status", api.RiskRead, true},
		"github-connect":    {"POST", "/v1/github/connect", "github connect", api.RiskWrite, false},
		"github-install":    {"POST", "/v1/github/install", "github install", api.RiskWrite, false},
		"github-use-app":    {"PUT", "/v1/github/app", "github use-app", api.RiskWrite, true},
		"github-disconnect": {"DELETE", "/v1/github", "github disconnect", api.RiskDestructive, false},
		"github-repos":      {"GET", "/v1/github/repos", "github repos", api.RiskRead, true},
		"github-repo":       {"GET", "/v1/github/repos/{owner}/{repo}", "github repo", api.RiskRead, true},
		"deploy-github":     {"POST", "/v1/projects/{project}/apps/{app}/deploys/github", "deploys github", api.RiskWrite, true},
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

// Restore drills: `backups drill` starts one, `backups drills` lists them
// and is also the group for get/start/cancel.
func TestDrillCommands(t *testing.T) {
	a := api.New(api.Deps{})
	want := map[string]struct{ method, path, cli, risk string }{
		"backup-drill":          {"POST", "/v1/backups/drill", "backups drill", api.RiskWrite},
		"backups-drills":        {"GET", "/v1/backups/drills", "backups drills", api.RiskRead},
		"backups-drills-get":    {"GET", "/v1/backups/drills/{id}", "backups drills get", api.RiskRead},
		"backups-drills-start":  {"POST", "/v1/backups/{id}/drill", "backups drills start", api.RiskWrite},
		"backups-drills-cancel": {"POST", "/v1/backups/drills/{id}/cancel", "backups drills cancel", api.RiskWrite},
	}
	for _, o := range a.Operations() {
		w, ok := want[o.OperationID]
		if !ok {
			continue
		}
		delete(want, o.OperationID)
		if o.Method != w.method || o.Path != w.path || strings.Join(api.CLIPath(o), " ") != w.cli || api.RiskOf(o) != w.risk {
			t.Errorf("%s: %s %s cli=%v risk=%s", o.OperationID, o.Method, o.Path, api.CLIPath(o), api.RiskOf(o))
		}
	}
	if len(want) > 0 {
		t.Errorf("missing operations: %v", want)
	}
	help := func(args ...string) string {
		var out, errb bytes.Buffer
		no := false
		cli.Execute(context.Background(), append(args, "--help"), cli.IO{Out: &out, Err: &errb, TTY: &no, Env: func(string) string { return "" }})
		return out.String() + errb.String()
	}
	if h := help("backups", "drills"); !strings.Contains(h, "List restore drills") || !strings.Contains(h, "cancel") || !strings.Contains(h, "start") {
		t.Errorf("backups drills --help:\n%s", h)
	}
	if h := help("backups", "drills", "get"); !strings.Contains(h, "Show a restore drill") {
		t.Errorf("backups drills get --help:\n%s", h)
	}
	if h := help("backups", "drill"); !strings.Contains(h, "newest backup") || !strings.Contains(h, "--wait") {
		t.Errorf("backups drill --help:\n%s", h)
	}
}
