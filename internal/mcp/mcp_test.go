package mcp_test

import (
	"encoding/json"
	"fmt"
	"github.com/danielgtaylor/huma/v2"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	tmcp "github.com/btahir/tiffin/internal/mcp"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, scopes ...tokens.Scope) *sdk.ClientSession {
	t.Helper()
	ctx := t.Context()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(ctx)
	secret := owner
	if len(scopes) > 0 {
		op, _ := tm.Authenticate(ctx, owner)
		secret, _, err = tm.Create(ctx, op, tokens.CreateRequest{Name: "agent", Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
	}
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Version: "test"})
	srv := tmcp.NewServer(a, a.Handler(), "test", tmcp.Static(secret))
	st, ct := sdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (*sdk.CallToolResult, map[string]any) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)
	return res, out
}

func TestToolsMirrorTheAPI(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var ops []*huma.Operation
	for _, o := range api.New(api.Deps{}).Operations() {
		if cp := api.CLIPath(o); len(cp) == 1 && cp[0] == "-" {
			continue // dashboard-only, not a tool
		}
		ops = append(ops, o)
	}
	if len(res.Tools) != len(ops)+1 { // +1: the code-mode "run" tool
		t.Fatalf("%d tools for %d operations", len(res.Tools), len(ops))
	}
	byName := map[string]*sdk.Tool{}
	for _, tl := range res.Tools {
		byName[tl.Name] = tl
	}
	for _, o := range ops {
		tl := byName[tmcp.ToolName(o.OperationID)]
		if tl == nil {
			t.Fatalf("no tool for %s", o.OperationID)
		}
		a := tl.Annotations
		switch api.RiskOf(o) {
		case api.RiskRead:
			if !a.ReadOnlyHint || !a.IdempotentHint {
				t.Errorf("%s: read op must be readOnly+idempotent", tl.Name)
			}
		case api.RiskDestructive:
			if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Errorf("%s: destructive op must say so", tl.Name)
			}
		}
		if a.OpenWorldHint == nil || *a.OpenWorldHint != api.IsOutbound(o) {
			t.Errorf("%s: openWorld must be true exactly for outbound ops (fetching from the internet)", tl.Name)
		}
	}
	// The apply tool carries the real manifest schema, with $defs resolvable.
	schema, _ := json.Marshal(byName["apply"].InputSchema)
	for _, want := range []string{`"confirm"`, `"intent"`, `"framework"`, `"$defs"`, `"#/$defs/slug"`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("apply schema lacks %s", want)
		}
	}
	if strings.Contains(string(schema), "#/components/") {
		t.Errorf("apply schema has unresolved component refs")
	}
	if !strings.Contains(string(schema), `"required":["manifest"]`) {
		t.Errorf("apply schema should require manifest: %s", schema)
	}
}

func TestPlanConfirmApplyUndoOverMCP(t *testing.T) {
	cs := connect(t)
	m := map[string]any{"project": "shop", "apps": map[string]any{"api": map[string]any{"framework": "hono"}}}

	res, plan := call(t, cs, "plan", map[string]any{"manifest": m})
	if res.IsError || plan["hash"] == nil {
		t.Fatalf("plan: %+v", res.Content)
	}
	res, prob := call(t, cs, "apply", map[string]any{"manifest": m, "intent": "add api"})
	if res.IsError || prob["code"] != "confirm_required" {
		t.Fatalf("apply without confirm should return the plan, not an error: %+v", prob)
	}
	if txt := res.Content[0].(*sdk.TextContent).Text; !strings.Contains(txt, "Nothing was applied") {
		t.Errorf("text: %s", txt)
	}
	res, out := call(t, cs, "apply", map[string]any{"manifest": m, "confirm": plan["hash"], "intent": "add api"})
	if res.IsError || out["applied"] != true {
		t.Fatalf("apply: %+v", out)
	}
	ch := out["change"].(map[string]any)
	if !strings.HasPrefix(ch["actor"].(map[string]any)["session"].(string), "mcp:") {
		t.Errorf("MCP session not recorded on the change: %v", ch["actor"])
	}
	_, list := call(t, cs, "changes_list", map[string]any{"project": "shop", "limit": 5})
	_ = list
	res, prob = call(t, cs, "change_undo", map[string]any{"id": ch["id"]})
	if res.IsError || prob["code"] != "confirm_required" {
		t.Fatalf("undo plan: %+v", prob)
	}
	uh := prob["plan"].(map[string]any)["hash"]
	res, out = call(t, cs, "change_undo", map[string]any{"id": ch["id"], "confirm": uh})
	if res.IsError || out["applied"] != true {
		t.Fatalf("undo: %+v", out)
	}
}

func TestErrorsAreToolErrors(t *testing.T) {
	cs := connect(t, tokens.ScopeRead)
	res, prob := call(t, cs, "plan", map[string]any{"manifest": map[string]any{"project": "shop"}})
	if !res.IsError || prob["code"] != "forbidden" {
		t.Fatalf("read-only token planning: %+v", prob)
	}
	res, prob = call(t, cs, "project_get", map[string]any{"project": "BAD!"})
	if !res.IsError || prob["code"] != "validation" {
		t.Fatalf("bad path arg: %+v", prob)
	}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "whoami", Arguments: map[string]any{"bogus": 1}})
	if err == nil && !res.IsError {
		t.Fatalf("unknown argument accepted")
	}
}

// MCP requires structuredContent to be a JSON object; clients such as Claude
// Code reject a tool result whose structuredContent is an array.
func TestStructuredContentIsAlwaysAnObject(t *testing.T) {
	cs := connect(t)
	for _, name := range []string{"projects_list", "changes_list", "tokens_list", "audit_list", "whoami"} {
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := res.StructuredContent.(map[string]any); !ok {
			t.Errorf("%s: structuredContent is %T, want an object", name, res.StructuredContent)
		}
	}
	_, out := call(t, cs, "tokens_list", map[string]any{})
	if items, ok := out["items"].([]any); !ok || len(items) != 1 {
		t.Errorf("tokens_list items: %v", out)
	}
}

// Only tools that take a confirm hash may promise that calling without one is
// safe; token_revoke acts immediately.
func TestOnlyConfirmableToolsClaimDryRun(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		schema, _ := json.Marshal(tl.InputSchema)
		takesConfirm := strings.Contains(string(schema), `"confirm"`)
		claims := strings.Contains(tl.Description, "without a confirm hash is always safe")
		if claims != takesConfirm {
			t.Errorf("%s: claims dry-run=%v but takes confirm=%v: %s", tl.Name, claims, takesConfirm, tl.Description)
		}
	}
}

func TestRunToolChainsCalls(t *testing.T) {
	cs := connect(t, tokens.ScopeRead, tokens.ScopePlan)
	res, out := call(t, cs, "run", map[string]any{"code": `
		const who = tiffin.call("whoami", {});
		const plan = tiffin.call("plan", {manifest: {project: "shop", services: {postgres: {}}}});
		console.log("planned", plan.ops.length, "ops");
		return {kind: who.kind, risk: plan.risk, hash: plan.hash.length};`})
	if res.IsError {
		t.Fatalf("run: %+v", out)
	}
	r := out["result"].(map[string]any)
	if r["kind"] != "agent" || r["risk"] != "reversible" || r["hash"].(float64) != 64 || out["calls"].(float64) != 2 {
		t.Fatalf("run result: %+v", out)
	}
	// Its powers are the caller's: a read/plan token cannot apply through run.
	res, out = call(t, cs, "run", map[string]any{"code": `
		const m = {project: "shop", services: {postgres: {}}};
		const p = tiffin.call("plan", {manifest: m});
		return tiffin.call("apply", {manifest: m, confirm: p.hash});`})
	if !res.IsError || !strings.Contains(fmt.Sprint(out["error"]), "forbidden") {
		t.Fatalf("run must not escalate: %+v", out)
	}
	// Runaway programs stop.
	res, out = call(t, cs, "run", map[string]any{"code": `for (let i = 0; i < 100; i++) tiffin.call("whoami", {});`})
	if !res.IsError || !strings.Contains(fmt.Sprint(out["error"]), "more than") {
		t.Fatalf("call cap: %+v", out)
	}
}
