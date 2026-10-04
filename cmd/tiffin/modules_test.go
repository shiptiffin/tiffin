package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	const wantOps = 194
	if n := len(a.Operations()); n != wantOps {
		t.Errorf("%d operations, want %d", n, wantOps)
	}
}

// The default MCP tool group names real tools, stays short, and splits SQL
// by risk: reading is read-only (no confirmation), writing is destructive.
func TestCoreToolsAndSQLRisk(t *testing.T) {
	byName := map[string]*mcp.Tool{}
	for _, tl := range mcp.Tools(api.New(api.Deps{})) {
		byName[tl.Tool.Name] = tl
	}
	for _, n := range mcp.CoreTools {
		if byName[n] == nil {
			t.Errorf("core tool %s is not an operation", n)
		}
	}
	if n := len(mcp.CoreTools); n < 30 || n > 40 {
		t.Errorf("%d core tools: keep the default set between 30 and 40", n)
	}
	r, w := byName["sql"], byName["sql_write"]
	if r == nil || w == nil {
		t.Fatal("want sql and sql_write tools")
	}
	if !r.Tool.Annotations.ReadOnlyHint || strings.Contains(r.Tool.Description, "confirm hash") {
		t.Errorf("sql must be read-only: %+v", r.Tool.Annotations)
	}
	if raw, _ := json.Marshal(r.Tool.InputSchema); strings.Contains(string(raw), `"write"`) {
		t.Error("sql takes no write flag any more")
	}
	if w.Tool.Annotations.ReadOnlyHint || w.Tool.Annotations.DestructiveHint == nil || !*w.Tool.Annotations.DestructiveHint {
		t.Errorf("sql_write must be destructive: %+v", w.Tool.Annotations)
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

// `tiffin sql` reads; `--write` (or `sql write`) goes to the write operation
// with the same inputs, and the SQL can be the last argument.
func TestSQLCommandRoutes(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, r.Method+" "+r.URL.Path+" "+fmt.Sprint(body["sql"]))
		_, _ = w.Write([]byte(`{"database":"shop","readOnly":true,"results":[],"durationMs":1}`))
	}))
	defer srv.Close()
	env := map[string]string{"TIFFIN_HOME": t.TempDir(), "HOME": t.TempDir(), "TIFFIN_CONFIG_DIR": t.TempDir(),
		"TIFFIN_URL": srv.URL, "TIFFIN_TOKEN": "tfn_x"}
	run := func(t *testing.T, env map[string]string, args ...string) (int, []byte, string) {
		var out, errb bytes.Buffer
		no := false
		code := cli.Execute(context.Background(), args, cli.IO{Out: &out, Err: &errb, TTY: &no, In: strings.NewReader(""),
			Env: func(k string) string { return env[k] }})
		return code, out.Bytes(), errb.String()
	}
	for _, args := range [][]string{
		{"sql", "shop", "select 1"},
		{"sql", "shop", "--sql", "select 2"},
		{"sql", "shop", "--write", "create table t()"},
		{"sql", "write", "shop", "drop table t"},
	} {
		if code, out, errs := run(t, env, args...); code != cli.ExitOK {
			t.Fatalf("%v: exit %d %s %s", args, code, out, errs)
		}
	}
	want := []string{
		"POST /v1/projects/shop/sql select 1",
		"POST /v1/projects/shop/sql select 2",
		"POST /v1/projects/shop/sql/write create table t()",
		"POST /v1/projects/shop/sql/write drop table t",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
