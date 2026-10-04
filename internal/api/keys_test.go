package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/tokens"
)

// keysEnv has two projects, shop and blog, each with a Postgres database,
// so dropping it is an irreversible plan.
func keysEnv(t *testing.T) *env {
	e := newEnv(t)
	for _, p := range []string{"shop", "blog"} {
		e.applyManifest(map[string]any{"project": p, "services": map[string]any{"postgres": map[string]any{}}})
	}
	return e
}

// dropDB plans dropping project's database with tok and applies it.
func (e *env) dropDB(tok, project string) (int, map[string]any) {
	e.t.Helper()
	m := map[string]any{"project": project}
	code, plan, _ := e.call(tok, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code != 200 {
		return code, plan
	}
	if plan["risk"] != "irreversible" {
		e.t.Fatalf("dropping the database should be irreversible: %v", plan)
	}
	code, out, _ := e.call(tok, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"], "intent": "drop the database"})
	return code, out
}

func noApprovalRequired(t *testing.T, prob map[string]any) {
	t.Helper()
	if prob["code"] == "approval_required" || prob["approval"] != nil || prob["approvalUrl"] != nil {
		t.Fatalf("approval path is gone, got %v", prob)
	}
}

// Every combination of projects (all, one, several) and access (full, read).
func TestAPIKeyReach(t *testing.T) {
	cases := []struct {
		name     string
		projects any
		access   string
		canApply map[string]bool // project → irreversible apply allowed
		canRead  map[string]bool
		admin    bool
		refusal  string // detail of a refused apply
	}{
		{"all/full", "all", "full", map[string]bool{"shop": true, "blog": true}, map[string]bool{"shop": true, "blog": true}, true, ""},
		{"all/read", "all", "read", map[string]bool{}, map[string]bool{"shop": true, "blog": true}, false, "is read only"},
		{"shop/full", []string{"shop"}, "full", map[string]bool{"shop": true}, map[string]bool{"shop": true}, false, "can only change shop"},
		{"shop/read", []string{"shop"}, "read", map[string]bool{}, map[string]bool{"shop": true}, false, "is read only"},
		{"shop+blog/full", []string{"shop", "blog"}, "full", map[string]bool{"shop": true, "blog": true}, map[string]bool{"shop": true, "blog": true}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := keysEnv(t)
			code, created, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "claude", "projects": c.projects, "access": c.access})
			if code != 200 {
				t.Fatalf("create: %d %v", code, created)
			}
			key := created["key"].(map[string]any)
			if key["access"] != c.access || key["admin"] != c.admin || key["expiresAt"] != nil || key["name"] != "claude" || key["id"] == nil || key["createdAt"] == nil {
				t.Fatalf("key: %v", key)
			}
			if c.projects == "all" && key["projects"] != "all" {
				t.Fatalf("projects: %v", key["projects"])
			}
			tok := created["secret"].(string)
			if _, who, _ := e.call(tok, "GET", "/v1/whoami", nil); who["access"] != c.access || who["kind"] != "agent" {
				t.Fatalf("whoami: %v", who)
			}
			for _, p := range []string{"shop", "blog"} {
				code, _, _ := e.call(tok, "GET", "/v1/projects/"+p, nil)
				if (code == 200) != c.canRead[p] {
					t.Fatalf("read %s: %d", p, code)
				}
				code, out := e.dropDB(tok, p)
				noApprovalRequired(t, out)
				if c.canApply[p] {
					if code != 200 || out["applied"] != true {
						t.Fatalf("drop %s: %d %v", p, code, out)
					}
					ch := out["change"].(map[string]any)
					actor := ch["actor"].(map[string]any)
					if actor["kind"] != "agent" || actor["name"] != "claude" || ch["intent"] != "drop the database" || ch["plan"].(map[string]any)["risk"] != "irreversible" {
						t.Fatalf("change: %v", ch)
					}
					// It is in History and can be undone.
					_, _, hist := e.call(e.owner, "GET", "/v1/changes?project="+p, nil)
					if hist[0].(map[string]any)["id"] != ch["id"] {
						t.Fatalf("history: %v", hist)
					}
					_, undo, _ := e.call(tok, "POST", "/v1/changes/"+ch["id"].(string)+"/undo", map[string]any{})
					if code, res, _ := e.call(tok, "POST", "/v1/changes/"+ch["id"].(string)+"/undo", map[string]any{"confirm": undo["plan"].(map[string]any)["hash"]}); code != 200 {
						t.Fatalf("undo: %d %v", code, res)
					}
					continue
				}
				if code != 403 || out["code"] != "forbidden" || out["hint"] == nil {
					t.Fatalf("drop %s should be refused: %d %v", p, code, out)
				}
				if detail := out["detail"].(string); c.canRead[p] && !strings.Contains(detail, c.refusal) {
					t.Fatalf("refusal for %s: %q", p, detail)
				}
			}
			code, _, _ = e.call(tok, "GET", "/v1/tokens", nil)
			if (code == 200) != c.admin {
				t.Fatalf("list keys: %d", code)
			}
			code, _, _ = e.call(tok, "POST", "/v1/people", map[string]any{"name": "Sam", "role": "member"})
			if (code == 200) != c.admin {
				t.Fatalf("invite a person: %d", code)
			}
		})
	}
}

// A refused apply in a read-only key's own project says it is read only.
func TestReadKeyRefusal(t *testing.T) {
	e := keysEnv(t)
	tok := e.key([]string{"shop"}, "read")
	code, out := e.dropDB(tok, "shop")
	if code != 403 || out["code"] != "forbidden" || !strings.Contains(out["detail"].(string), "read only") || out["plan"] == nil {
		t.Fatalf("read-only apply: %d %v", code, out)
	}
	code, out, _ = e.call(tok, "GET", "/v1/projects/blog", nil)
	if code != 403 || !strings.Contains(out["detail"].(string), "can only read shop") {
		t.Fatalf("read other project: %d %v", code, out)
	}
}

// "all" includes projects created later.
func TestAllProjectsIncludesNewOnes(t *testing.T) {
	e := newEnv(t)
	tok := e.key("all", "full")
	m := map[string]any{"project": "later", "services": map[string]any{"postgres": map[string]any{}}}
	_, plan, _ := e.call(tok, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code, out, _ := e.call(tok, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]}); code != 200 {
		t.Fatalf("new project: %d %v", code, out)
	}
	if code, out := e.dropDB(tok, "later"); code != 200 {
		t.Fatalf("drop: %d %v", code, out)
	}
}

func TestAPIKeyCreateValidation(t *testing.T) {
	e := newEnv(t)
	for _, body := range []map[string]any{
		{"name": "x", "projects": "everything", "access": "full"},
		{"name": "x", "projects": []string{}, "access": "full"},
		{"name": "x", "projects": []string{"Not A Project"}, "access": "full"},
		{"name": "x", "projects": "all", "access": "deploy"},
		{"name": "x", "projects": "all"},
		{"name": "x", "access": "full"},
		{"name": "x", "projects": "all", "access": "full", "expiresInDays": 7},
		{"name": "x", "projects": "all", "access": "full", "scopes": []string{"read"}},
	} {
		if code, out, _ := e.call(e.owner, "POST", "/v1/tokens", body); code != 422 {
			t.Errorf("%v: %d %v", body, code, out)
		}
	}
	for _, days := range []int{30, 90} {
		code, out, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "ci", "projects": []string{"shop"}, "access": "read", "expiresInDays": days})
		if code != 200 {
			t.Fatalf("%d days: %d %v", days, code, out)
		}
		exp, _ := time.Parse(time.RFC3339Nano, out["key"].(map[string]any)["expiresAt"].(string))
		if d := time.Until(exp) - time.Duration(days)*24*time.Hour; d > time.Minute || d < -time.Minute {
			t.Fatalf("%d days: expires %v", days, exp)
		}
	}
	code, out, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "forever", "projects": "all", "access": "full", "expiresInDays": nil})
	if code != 200 || out["key"].(map[string]any)["expiresAt"] != nil {
		t.Fatalf("never expires: %d %v", code, out)
	}
}

// Tokens minted before API keys keep their exact powers and show as the
// nearest key, with a note when they can do less.
func TestOlderTokensKeepWorking(t *testing.T) {
	e := keysEnv(t)
	old := e.legacy("old-agent", []tokens.Scope{tokens.ScopeRead, tokens.ScopePlan, tokens.ScopeApplyReversible}, []string{"shop"})
	m := map[string]any{"project": "shop", "env": map[string]any{"A": "1"}, "services": map[string]any{"postgres": map[string]any{}}}
	_, plan, _ := e.call(old, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code, out, _ := e.call(old, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]}); code != 200 {
		t.Fatalf("reversible: %d %v", code, out)
	}
	code, out := e.dropDB(old, "shop")
	noApprovalRequired(t, out)
	if code != 403 || out["code"] != "forbidden" || !strings.Contains(out["detail"].(string), "cannot apply irreversible") {
		t.Fatalf("irreversible: %d %v", code, out)
	}
	_, _, list := e.call(e.owner, "GET", "/v1/tokens", nil)
	var seen bool
	for _, k := range list {
		k := k.(map[string]any)
		switch k["name"] {
		case "old-agent":
			seen = true
			if k["access"] != "full" || k["projects"].([]any)[0] != "shop" || !strings.Contains(k["note"].(string), "reversible changes only") {
				t.Fatalf("old token as a key: %v", k)
			}
		case "owner":
			if k["projects"] != "all" || k["access"] != "full" || k["admin"] != true {
				t.Fatalf("owner as a key: %v", k)
			}
		}
		if _, has := k["scopes"]; has {
			t.Fatalf("scopes are not part of a key: %v", k)
		}
	}
	if !seen {
		t.Fatalf("old token not listed: %v", list)
	}
}

// The approval layer is gone from the API, the CLI and MCP.
func TestNoApprovalOperations(t *testing.T) {
	a := api.New(api.Deps{})
	for _, o := range a.Operations() {
		if strings.Contains(o.Path, "approval") || strings.Contains(o.OperationID, "approval") {
			t.Errorf("approval operation still exposed: %s %s", o.OperationID, o.Path)
		}
	}
	e := newEnv(t)
	res, err := http.Get(e.srv.URL + "/v1/approvals")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("GET /v1/approvals: %d", res.StatusCode)
	}
}
