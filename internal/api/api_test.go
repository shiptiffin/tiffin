package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/approvals"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

type env struct {
	t     *testing.T
	srv   *httptest.Server
	owner string
	tm    *tokens.Manager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Version: "test"})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, owner: owner, tm: tm}
}

// call does a request and decodes the JSON response into a generic map/slice.
func (e *env) call(token, method, path string, body any) (int, map[string]any, []any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.SessionHeader, "sess-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var obj map[string]any
	var arr []any
	if len(raw) > 0 {
		if raw[0] == '[' {
			_ = json.Unmarshal(raw, &arr)
		} else if err := json.Unmarshal(raw, &obj); err != nil {
			e.t.Fatalf("%s %s: non-JSON response %q", method, path, raw)
		}
	}
	return res.StatusCode, obj, arr
}

func (e *env) agent(scopes []string, projects []string) string {
	e.t.Helper()
	code, out, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "claude", "scopes": scopes, "projects": projects})
	if code != 200 {
		e.t.Fatalf("create token: %d %v", code, out)
	}
	return out["secret"].(string)
}

var shop = map[string]any{
	"project":  "shop",
	"apps":     map[string]any{"web": map[string]any{"framework": "next"}},
	"services": map[string]any{"postgres": map[string]any{"extensions": []string{"vector"}}},
}

func TestHealthAndSchemaArePublic(t *testing.T) {
	e := newEnv(t)
	if code, out, _ := e.call("", "GET", "/v1/health", nil); code != 200 || out["status"] != "ok" {
		t.Fatalf("health: %d %v", code, out)
	}
	if code, out, _ := e.call("", "GET", "/v1/schema/manifest", nil); code != 200 || out["$id"] == nil {
		t.Fatalf("schema: %d", code)
	}
	if code, out, _ := e.call("", "GET", "/v1/openapi.json", nil); code != 200 || out["openapi"] == nil {
		t.Fatalf("openapi: %d", code)
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	for _, tok := range []string{"", "tfn_bogus", "not-a-token"} {
		code, out, _ := e.call(tok, "GET", "/v1/whoami", nil)
		if code != 401 || out["code"] != "unauthenticated" {
			t.Errorf("token %q: %d %v", tok, code, out)
		}
	}
	code, out, _ := e.call(e.owner, "GET", "/v1/whoami", nil)
	if code != 200 || out["kind"] != "owner" || out["session"] != "sess-1" {
		t.Fatalf("whoami: %d %v", code, out)
	}
}

func TestPlanApplyConfirmFlow(t *testing.T) {
	e := newEnv(t)
	code, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": shop})
	if code != 200 || plan["risk"] != "reversible" || len(plan["ops"].([]any)) != 3 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	hash := plan["hash"].(string)

	code, prob, _ := e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": shop})
	if code != 428 || prob["code"] != "confirm_required" || prob["plan"].(map[string]any)["hash"] != hash || prob["hint"] == "" {
		t.Fatalf("apply without confirm: %d %v", code, prob)
	}
	code, prob, _ = e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": shop, "confirm": "0123456789ab"})
	if code != 428 || prob["code"] != "plan_mismatch" {
		t.Fatalf("apply with wrong confirm: %d %v", code, prob)
	}
	code, res, _ := e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": shop, "confirm": hash[:12], "intent": "launch"})
	if code != 200 || res["applied"] != true {
		t.Fatalf("apply: %d %v", code, res)
	}
	ch := res["change"].(map[string]any)
	if ch["intent"] != "launch" || ch["actor"].(map[string]any)["session"] != "sess-1" {
		t.Fatalf("change record: %v", ch)
	}
	code, res, _ = e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": shop, "confirm": hash})
	if code != 200 || res["applied"] != false {
		t.Fatalf("idempotent re-apply: %d %v", code, res)
	}
	code, st, _ := e.call(e.owner, "GET", "/v1/projects/shop", nil)
	if code != 200 || st["version"].(float64) != 1 || len(st["resources"].([]any)) != 3 {
		t.Fatalf("project: %d %v", code, st)
	}
	// Undo: 428 with the undo plan, then confirm.
	id := ch["id"].(string)
	code, prob, _ = e.call(e.owner, "POST", "/v1/changes/"+id+"/undo", map[string]any{})
	if code != 428 || prob["plan"].(map[string]any)["risk"] != "irreversible" {
		t.Fatalf("undo plan: %d %v", code, prob)
	}
	uh := prob["plan"].(map[string]any)["hash"].(string)
	code, res, _ = e.call(e.owner, "POST", "/v1/changes/"+id+"/undo", map[string]any{"confirm": uh})
	if code != 200 || res["change"].(map[string]any)["undoOf"] != id {
		t.Fatalf("undo: %d %v", code, res)
	}
	code, prob, _ = e.call(e.owner, "POST", "/v1/changes/"+id+"/undo", map[string]any{"confirm": uh})
	if code != 409 || prob["code"] != "precondition" {
		t.Fatalf("double undo: %d %v", code, prob)
	}
	_, _, list := e.call(e.owner, "GET", "/v1/changes?project=shop", nil)
	if len(list) != 2 {
		t.Fatalf("changes: %v", list)
	}
}

func TestValidationErrorsPointAtFields(t *testing.T) {
	e := newEnv(t)
	bad := map[string]any{"project": "Shop!", "apps": map[string]any{"w": map[string]any{"instances": 99, "framework": "rails"}}}
	code, prob, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": bad})
	if code != 422 || prob["code"] != "validation" {
		t.Fatalf("%d %v", code, prob)
	}
	paths := map[string]bool{}
	for _, fe := range prob["errors"].([]any) {
		paths[fe.(map[string]any)["path"].(string)] = true
	}
	for _, want := range []string{"/manifest/project", "/manifest/apps/w/instances", "/manifest/apps/w/framework"} {
		if !paths[want] {
			t.Errorf("missing field error %s in %v", want, paths)
		}
	}
	if code, prob, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{}); code != 422 {
		t.Errorf("missing manifest: %d %v", code, prob)
	}
}

func TestAgentScopesEnforced(t *testing.T) {
	e := newEnv(t)
	// Owner sets up two projects.
	for _, p := range []string{"shop", "blog"} {
		m := map[string]any{"project": p, "services": map[string]any{"postgres": map[string]any{}}}
		_, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": m})
		if code, out, _ := e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]}); code != 200 {
			t.Fatalf("setup %s: %d %v", p, code, out)
		}
	}
	agent := e.agent(nil, []string{"shop"}) // defaults: read, plan, apply:reversible

	// Reversible change on its project: allowed.
	m := map[string]any{"project": "shop", "env": map[string]any{"A": "1"}, "services": map[string]any{"postgres": map[string]any{}}}
	_, plan, _ := e.call(agent, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code, out, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]}); code != 200 {
		t.Fatalf("agent reversible apply: %d %v", code, out)
	}
	// Irreversible (drop postgres): denied, with the plan attached for a human.
	drop := map[string]any{"project": "shop", "env": map[string]any{"A": "1"}}
	_, plan, _ = e.call(agent, "POST", "/v1/plan", map[string]any{"manifest": drop})
	code, prob, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": plan["hash"]})
	if code != 403 || prob["code"] != "denied" || prob["plan"] == nil {
		t.Fatalf("agent irreversible: %d %v", code, prob)
	}
	// Other project: forbidden even to plan or read.
	if code, prob, _ := e.call(agent, "POST", "/v1/plan", map[string]any{"manifest": map[string]any{"project": "blog"}}); code != 403 {
		t.Fatalf("plan other project: %d %v", code, prob)
	}
	if code, _, _ := e.call(agent, "GET", "/v1/projects/blog", nil); code != 403 {
		t.Fatalf("read other project: %d", code)
	}
	_, _, projects := e.call(agent, "GET", "/v1/projects", nil)
	if len(projects) != 1 {
		t.Fatalf("agent sees projects %v", projects)
	}
	_, _, all := e.call(e.owner, "GET", "/v1/changes?project=blog", nil)
	blogChange := all[0].(map[string]any)["id"].(string)
	if code, _, _ := e.call(agent, "GET", "/v1/changes/"+blogChange, nil); code != 404 {
		t.Fatalf("other project's change must look missing: %d", code)
	}
	if code, prob, _ := e.call(agent, "POST", "/v1/changes/"+blogChange+"/undo", map[string]any{}); code != 404 || strings.Contains(fmt.Sprint(prob), "blog") {
		t.Fatalf("undo other project's change must look missing too: %d %v", code, prob)
	}
	_, _, list := e.call(agent, "GET", "/v1/changes", nil)
	for _, c := range list {
		if c.(map[string]any)["project"] != "shop" {
			t.Fatalf("agent sees foreign change %v", c)
		}
	}
	// Busy foreign projects must not crowd a scoped token's own changes out
	// of the limit.
	for _, v := range []string{"1", "2", "3"} {
		bm := map[string]any{"project": "blog", "env": map[string]any{"V": v}, "services": map[string]any{"postgres": map[string]any{}}}
		_, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": bm})
		e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": bm, "confirm": plan["hash"]})
	}
	if _, _, list := e.call(agent, "GET", "/v1/changes?limit=2", nil); len(list) != 2 || list[0].(map[string]any)["project"] != "shop" {
		t.Fatalf("scoped token's change list: %v", list)
	}
	// No token admin, no escalation.
	if code, _, _ := e.call(agent, "GET", "/v1/tokens", nil); code != 403 {
		t.Fatalf("agent lists tokens: %d", code)
	}
	if code, _, _ := e.call(agent, "POST", "/v1/tokens", map[string]any{"name": "x"}); code != 403 {
		t.Fatalf("agent mints token: %d", code)
	}
	// Read-only token cannot plan.
	ro := e.agent([]string{"read"}, []string{"shop"})
	if code, _, _ := e.call(ro, "POST", "/v1/plan", map[string]any{"manifest": m}); code != 403 {
		t.Fatalf("read-only plan: %d", code)
	}
	// Audit shows the minting.
	_, _, audit := e.call(e.owner, "GET", "/v1/audit", nil)
	if len(audit) < 2 || !strings.HasPrefix(audit[0].(map[string]any)["action"].(string), "token.") {
		t.Fatalf("audit: %v", audit)
	}
}

func TestTokenRevoke(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(nil, nil)
	_, who, _ := e.call(agent, "GET", "/v1/whoami", nil)
	id := who["tokenId"].(string)
	if code, out, _ := e.call(e.owner, "DELETE", "/v1/tokens/"+id, nil); code != 204 && code != 200 {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if code, _, _ := e.call(agent, "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
}

func TestEveryOperationIsAnnotated(t *testing.T) {
	a := api.New(api.Deps{})
	seen := map[string]bool{}
	for _, o := range a.Operations() {
		r := api.RiskOf(o)
		if r != api.RiskRead && r != api.RiskWrite && r != api.RiskDestructive {
			t.Errorf("%s: bad risk %q", o.OperationID, r)
		}
		if o.Summary == "" || o.Description == "" {
			t.Errorf("%s: missing summary/description (agents read these)", o.OperationID)
		}
		cli := strings.Join(api.CLIPath(o), " ")
		if seen[cli] {
			t.Errorf("duplicate CLI path %q", cli)
		}
		seen[cli] = true
	}
	if len(seen) < 12 {
		t.Fatalf("only %d operations", len(seen))
	}
}

func TestTokenAdminRespectsProjectScope(t *testing.T) {
	e := newEnv(t)
	// A delegate that may mint tokens, but only for "blog".
	code, out, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "blog-lead", "scopes": []string{"*"}, "projects": []string{"blog"}})
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	lead := out["secret"].(string)
	other := e.agent(nil, []string{"shop"}) // minted by the owner
	_, who, _ := e.call(other, "GET", "/v1/whoami", nil)

	_, _, list := e.call(lead, "GET", "/v1/tokens", nil)
	if len(list) != 0 {
		t.Fatalf("project-limited admin sees tokens it did not mint: %v", list)
	}
	if code, _, _ := e.call(lead, "GET", "/v1/audit", nil); code != 403 {
		t.Fatalf("project-limited admin reads audit: %d", code)
	}
	if code, _, _ := e.call(lead, "DELETE", "/v1/tokens/"+who["tokenId"].(string), nil); code != 403 {
		t.Fatalf("project-limited admin revokes a foreign token: %d", code)
	}
	// It does see and manage what it minted.
	e.call(lead, "POST", "/v1/tokens", map[string]any{"name": "blog-bot"})
	if _, _, list := e.call(lead, "GET", "/v1/tokens", nil); len(list) != 1 {
		t.Fatalf("lead should see its own token: %v", list)
	}
}

func TestAgentApprovalFlow(t *testing.T) {
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(t.Context())
	am, err := approvals.New(db, "dashboard.tiffin.localhost", "https://dashboard.tiffin.localhost:8443")
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Approvals: am, PublicURL: "https://dashboard.tiffin.localhost:8443"})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	e := &env{t: t, srv: srv, owner: owner, tm: tm}

	m := map[string]any{"project": "shop", "services": map[string]any{"postgres": map[string]any{}}}
	_, plan, _ := e.call(owner, "POST", "/v1/plan", map[string]any{"manifest": m})
	e.call(owner, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]})

	agent := e.agent(nil, []string{"shop"})
	drop := map[string]any{"project": "shop"}
	_, plan, _ = e.call(agent, "POST", "/v1/plan", map[string]any{"manifest": drop})
	hash := plan["hash"].(string)
	code, prob, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": hash, "intent": "drop db"})
	if code != 403 || prob["code"] != "approval_required" || !strings.Contains(prob["approvalUrl"].(string), "/approvals/apr_") {
		t.Fatalf("want approval_required: %d %v", code, prob)
	}
	id := prob["approval"].(map[string]any)["id"].(string)
	// Asking again returns the same pending request.
	_, prob2, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": hash})
	if prob2["approval"].(map[string]any)["id"] != id {
		t.Fatal("duplicate approval request")
	}
	// Pending approvals cannot be spent; agents cannot approve.
	if code, _, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": hash, "approval": id}); code != 403 {
		t.Fatalf("spent a pending approval: %d", code)
	}
	if code, _, _ := e.call(agent, "POST", "/v1/approvals/"+id+"/begin", nil); code != 403 {
		t.Fatalf("agent began approving: %d", code)
	}
	// The owner has no passkey yet: approving must say so.
	if code, p, _ := e.call(owner, "POST", "/v1/approvals/"+id+"/begin", nil); code != 409 || !strings.Contains(p["detail"].(string), "passkey") {
		t.Fatalf("no-passkey approve: %d %v", code, p)
	}
	// Simulate a successful passkey ceremony (covered by go-webauthn and the dashboard e2e).
	if _, err := db.SQL().Exec(`UPDATE approvals SET status = 'approved', decided_by = 'test' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	// Another agent can't use it.
	other := e.agent(nil, []string{"shop"})
	if code, _, _ := e.call(other, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": hash, "approval": id}); code != 403 {
		t.Fatalf("other agent spent approval: %d", code)
	}
	code, res, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": hash, "approval": id, "intent": "drop db"})
	if code != 200 || res["applied"] != true {
		t.Fatalf("approved apply: %d %v", code, res)
	}
	if code, _, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": hash, "approval": id}); code == 200 {
		t.Fatal("approval reused")
	}
	_, got, _ := e.call(agent, "GET", "/v1/approvals/"+id, nil)
	if got["status"] != "used" || got["usedBy"] == "" {
		t.Fatalf("approval after use: %v", got)
	}
}

func TestPeopleRolesAndInvites(t *testing.T) {
	e := newEnv(t)
	code, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Sam", "email": "sam@example.com", "role": "member"})
	if code != 200 || !strings.Contains(inv["url"].(string), "/login#tfl_") {
		t.Fatalf("invite: %d %v", code, inv)
	}
	person := inv["person"].(map[string]any)["id"].(string)
	codeStr := strings.SplitN(inv["url"].(string), "#", 2)[1]
	// Redeem the invite like the dashboard does.
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/session", strings.NewReader(`{"code":"`+codeStr+`"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("redeem: %v %d", err, res.StatusCode)
	}
	var session string
	for _, c := range res.Cookies() {
		if c.Name == api.SessionCookie {
			session = c.Value
		}
	}
	res.Body.Close()
	_, who, _ := e.call(session, "GET", "/v1/whoami", nil)
	if who["role"] != "member" || who["personName"] != "Sam" || who["name"] != "Sam" {
		t.Fatalf("member session: %v", who)
	}
	// The same link does not work twice.
	res2, _ := http.Post(e.srv.URL+"/v1/session", "application/json", strings.NewReader(`{"code":"`+codeStr+`"}`))
	if res2.StatusCode != 401 {
		t.Fatalf("reused invite: %d", res2.StatusCode)
	}
	// Members can plan and apply reversible changes, not irreversible ones, and can't manage people.
	m := map[string]any{"project": "shop", "services": map[string]any{"postgres": map[string]any{}}}
	_, plan, _ := e.call(session, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code, out, _ := e.call(session, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"], "intent": "by Sam"}); code != 200 || out["change"].(map[string]any)["actor"].(map[string]any)["name"] != "Sam" {
		t.Fatalf("member apply: %d %v", code, out)
	}
	drop := map[string]any{"project": "shop"}
	_, plan, _ = e.call(session, "POST", "/v1/plan", map[string]any{"manifest": drop})
	if code, _, _ := e.call(session, "POST", "/v1/apply", map[string]any{"manifest": drop, "confirm": plan["hash"]}); code != 403 {
		t.Fatalf("member irreversible: %d", code)
	}
	if code, _, _ := e.call(session, "POST", "/v1/people", map[string]any{"name": "X", "role": "admin"}); code != 403 {
		t.Fatalf("member invited someone: %d", code)
	}
	// Demote to viewer: the session ends at once.
	if code, _, _ := e.call(e.owner, "PATCH", "/v1/people/"+person, map[string]any{"role": "viewer"}); code != 200 {
		t.Fatalf("demote: %d", code)
	}
	if code, _, _ := e.call(session, "GET", "/v1/whoami", nil); code != 401 {
		t.Fatalf("session survived a role change: %d", code)
	}
	if code, _, _ := e.call(e.owner, "DELETE", "/v1/people/"+person, nil); code != 204 && code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if code, _, _ := e.call(e.owner, "DELETE", "/v1/people/usr_owner", nil); code != 403 {
		t.Fatalf("owner removable: %d", code)
	}
	_, _, people := e.call(e.owner, "GET", "/v1/people", nil)
	if len(people) != 2 || people[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("people: %v", people)
	}
}

func TestProjectDestroy(t *testing.T) {
	e := newEnv(t)
	_, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": shop})
	e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": shop, "confirm": plan["hash"]})
	code, prob, _ := e.call(e.owner, "POST", "/v1/projects/shop/destroy", map[string]any{})
	p, _ := prob["plan"].(map[string]any)
	if code != 428 || p["risk"] != "irreversible" || len(p["ops"].([]any)) != 3 {
		t.Fatalf("destroy plan: %d %v", code, prob)
	}
	if code, out, _ := e.call(e.owner, "POST", "/v1/projects/shop/destroy", map[string]any{"confirm": p["hash"]}); code != 200 || out["applied"] != true {
		t.Fatalf("destroy: %d %v", code, out)
	}
	if _, _, list := e.call(e.owner, "GET", "/v1/projects", nil); len(list) != 0 {
		t.Fatalf("projects after destroy: %v", list)
	}
	if code, _, _ := e.call(e.owner, "POST", "/v1/projects/nope/destroy", map[string]any{}); code != 404 {
		t.Fatalf("destroy missing: %d", code)
	}
}
