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
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/page"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

type env struct {
	t     *testing.T
	srv   *httptest.Server
	owner string
	tm    *tokens.Manager
	db    *state.DB
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
	return &env{t: t, srv: srv, owner: owner, tm: tm, db: db}
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
	req.Header.Set(api.ModelHeader, "claude-opus-5-5")
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

// page reads one page of a list: its items and the cursor for the next.
func (e *env) page(token, path string) ([]any, string) {
	e.t.Helper()
	code, out, _ := e.call(token, "GET", path, nil)
	if code != 200 {
		e.t.Fatalf("GET %s: %d %v", path, code, out)
	}
	items, _ := out["items"].([]any)
	next, _ := out["nextCursor"].(string)
	return items, next
}

// key creates an API key: projects is "all" or a list, access full or read.
func (e *env) key(projects any, access string) string {
	e.t.Helper()
	code, out, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "claude", "projects": projects, "access": access})
	if code != 200 {
		e.t.Fatalf("create key: %d %v", code, out)
	}
	return out["secret"].(string)
}

// legacy mints a token the way boxes did before API keys (exact scopes).
func (e *env) legacy(name string, scopes []tokens.Scope, projects []string) string {
	e.t.Helper()
	owner, err := e.tm.Authenticate(e.t.Context(), e.owner)
	if err != nil {
		e.t.Fatal(err)
	}
	secret, _, err := e.tm.Create(e.t.Context(), owner, tokens.CreateRequest{Name: name, Kind: tokens.KindAgent, Scopes: scopes, Projects: projects})
	if err != nil {
		e.t.Fatal(err)
	}
	return secret
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
	// A box answers before its modules have started; self-update waits for ok.
	box, _, _ := secretsBox(t)
	if code, out, _ := box.call("", "GET", "/v1/health", nil); code != 200 || out["status"] != "starting" {
		t.Fatalf("health before the platform started: %d %v", code, out)
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
	// The project, its app and its always-on parts (5 services and the files bucket).
	if code != 200 || plan["risk"] != "reversible" || len(plan["ops"].([]any)) != 8 {
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
	if _, twice := res["plan"]; twice || ch["plan"].(map[string]any)["hash"] != hash {
		t.Fatalf("the applied plan belongs in change.plan only: %v", res)
	}
	code, res, _ = e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": shop, "confirm": hash})
	if code != 200 || res["applied"] != false || res["plan"] == nil {
		t.Fatalf("idempotent re-apply: %d %v", code, res)
	}
	code, st, _ := e.call(e.owner, "GET", "/v1/projects/shop", nil)
	if code != 200 || st["version"].(float64) != 1 || len(st["resources"].([]any)) != 8 {
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
	list, _ := e.page(e.owner, "/v1/changes?project=shop")
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
	agent := e.key([]string{"shop"}, "full")

	// Reversible change on its project: allowed.
	m := map[string]any{"project": "shop", "env": map[string]any{"A": "1"}, "services": map[string]any{"postgres": map[string]any{}}}
	_, plan, _ := e.call(agent, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code, out, _ := e.call(agent, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]}); code != 200 {
		t.Fatalf("agent reversible apply: %d %v", code, out)
	}
	// Other project: forbidden even to plan or read, with a plain reason.
	if code, prob, _ := e.call(agent, "POST", "/v1/plan", map[string]any{"manifest": map[string]any{"project": "blog"}}); code != 403 ||
		prob["code"] != "forbidden" || !strings.Contains(prob["detail"].(string), "can only reach shop") || prob["hint"] == nil {
		t.Fatalf("plan other project: %d %v", code, prob)
	}
	if code, _, _ := e.call(agent, "GET", "/v1/projects/blog", nil); code != 403 {
		t.Fatalf("read other project: %d", code)
	}
	_, _, projects := e.call(agent, "GET", "/v1/projects", nil)
	if len(projects) != 1 {
		t.Fatalf("agent sees projects %v", projects)
	}
	all, _ := e.page(e.owner, "/v1/changes?project=blog")
	blogChange := all[0].(map[string]any)["id"].(string)
	if code, _, _ := e.call(agent, "GET", "/v1/changes/"+blogChange, nil); code != 404 {
		t.Fatalf("other project's change must look missing: %d", code)
	}
	if code, prob, _ := e.call(agent, "POST", "/v1/changes/"+blogChange+"/undo", map[string]any{}); code != 404 || strings.Contains(fmt.Sprint(prob), "blog") {
		t.Fatalf("undo other project's change must look missing too: %d %v", code, prob)
	}
	list, _ := e.page(agent, "/v1/changes")
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
	if list, _ := e.page(agent, "/v1/changes?limit=2"); len(list) != 2 || list[0].(map[string]any)["project"] != "shop" {
		t.Fatalf("scoped token's change list: %v", list)
	}
	// No key management, no escalation.
	if code, _, _ := e.call(agent, "GET", "/v1/tokens", nil); code != 403 {
		t.Fatalf("agent lists keys: %d", code)
	}
	if code, _, _ := e.call(agent, "POST", "/v1/tokens", map[string]any{"name": "x", "projects": []string{"shop"}, "access": "full"}); code != 403 {
		t.Fatalf("agent mints a key: %d", code)
	}
	// An older read-only token (no plan scope) still cannot plan.
	ro := e.legacy("ro", []tokens.Scope{tokens.ScopeRead}, []string{"shop"})
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
	agent := e.key("all", "full")
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
		if cli == "-" {
			continue // dashboard-only operation
		}
		if seen[cli] {
			t.Errorf("duplicate CLI path %q", cli)
		}
		seen[cli] = true
	}
	if len(seen) < 12 {
		t.Fatalf("only %d operations", len(seen))
	}
}

// Only a key with full access to all projects manages keys. An older
// token that could mint tokens for one project no longer manages any.
func TestOnlyTheBoxAdminManagesKeys(t *testing.T) {
	e := newEnv(t)
	lead := e.legacy("blog-lead", []tokens.Scope{tokens.ScopeAll}, []string{"blog"})
	other := e.key([]string{"shop"}, "full")
	_, who, _ := e.call(other, "GET", "/v1/whoami", nil)
	for _, tok := range []string{lead, other, e.key("all", "read")} {
		if code, _, _ := e.call(tok, "GET", "/v1/tokens", nil); code != 403 {
			t.Fatalf("lists keys: %d", code)
		}
		if code, _, _ := e.call(tok, "GET", "/v1/audit", nil); code != 403 {
			t.Fatalf("reads audit: %d", code)
		}
		if code, prob, _ := e.call(tok, "DELETE", "/v1/tokens/"+who["tokenId"].(string), nil); code != 403 || !strings.Contains(prob["detail"].(string), "full access to all projects") {
			t.Fatalf("revokes a key: %d %v", code, prob)
		}
		if code, _, _ := e.call(tok, "POST", "/v1/tokens", map[string]any{"name": "bot", "projects": []string{"blog"}, "access": "read"}); code != 403 {
			t.Fatalf("creates a key: %d", code)
		}
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
	if code, out := e.dropDB(session, "shop"); code != 403 {
		t.Fatalf("member irreversible: %d %v", code, out)
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
	if code != 428 || p["risk"] != "irreversible" || len(p["ops"].([]any)) != 8 {
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

func TestChangesPagingAndAgentModel(t *testing.T) {
	e := newEnv(t)
	apply := func(token string, env map[string]any) map[string]any {
		t.Helper()
		m := map[string]any{"project": "shop", "env": env}
		_, plan, _ := e.call(token, "POST", "/v1/plan", map[string]any{"manifest": m})
		code, res, _ := e.call(token, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]})
		if code != 200 {
			t.Fatalf("apply: %d %v", code, res)
		}
		return res["change"].(map[string]any)
	}
	first := apply(e.owner, map[string]any{"A": "1"})
	if _, ok := first["actor"].(map[string]any)["model"]; ok {
		t.Fatalf("a person's change carries no model: %v", first["actor"])
	}
	agent := e.key([]string{"shop"}, "full")
	apply(agent, map[string]any{"A": "2"})
	third := apply(agent, map[string]any{"A": "3"})
	if third["actor"].(map[string]any)["model"] != "claude-opus-5-5" {
		t.Fatalf("agent model: %v", third["actor"])
	}
	first2, next := e.page(e.owner, "/v1/changes?limit=2")
	if len(first2) != 2 || first2[0].(map[string]any)["id"] != third["id"] || next == "" {
		t.Fatalf("first page: %v %q", first2, next)
	}
	second, next := e.page(e.owner, "/v1/changes?limit=2&cursor="+next)
	if len(second) != 1 || second[0].(map[string]any)["id"] != first["id"] || next != "" {
		t.Fatalf("second page: %v %q", second, next)
	}
	// Filters keep working with the cursor: the agent's two, a page at a time.
	ag, next := e.page(e.owner, "/v1/changes?actor=agent&limit=1")
	if len(ag) != 1 || ag[0].(map[string]any)["id"] != third["id"] || next == "" {
		t.Fatalf("agent page 1: %v %q", ag, next)
	}
	ag, next = e.page(e.owner, "/v1/changes?actor=agent&limit=1&cursor="+next)
	if len(ag) != 1 || ag[0].(map[string]any)["actor"].(map[string]any)["kind"] != "agent" || next != "" {
		t.Fatalf("agent page 2: %v %q", ag, next)
	}
	if people, _ := e.page(e.owner, "/v1/changes?actor=people"); len(people) != 1 || people[0].(map[string]any)["id"] != first["id"] {
		t.Fatalf("people: %v", people)
	}
	if none, _ := e.page(e.owner, "/v1/changes?risk=irreversible"); len(none) != 0 {
		t.Fatalf("risk filter: %v", none)
	}
	for _, bad := range []string{"cursor=chg_00000000000000000000000000", "cursor=" + page.Encode("chg_00000000000000000000000000"), "limit=201"} {
		if code, prob, _ := e.call(e.owner, "GET", "/v1/changes?"+bad, nil); code != 422 || prob["code"] != "validation" {
			t.Fatalf("%s: %d %v", bad, code, prob)
		}
	}
}

func TestCookieChangesOnlyFromTheDashboard(t *testing.T) {
	e := newEnv(t)
	code, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Sam", "email": "sam@example.com", "role": "member"})
	if code != 200 {
		t.Fatalf("invite: %d", code)
	}
	codeStr := strings.SplitN(inv["url"].(string), "#", 2)[1]
	res, err := http.Post(e.srv.URL+"/v1/session", "application/json", strings.NewReader(`{"code":"`+codeStr+`"}`))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("redeem: %v", err)
	}
	res.Body.Close()
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == api.SessionCookie {
			cookie = c
		}
	}
	// refused reports whether the CSRF check (not the member's role) refused it.
	refused := func(method string, headers ...string) bool {
		req, _ := http.NewRequest(method, e.srv.URL+"/v1/plan", strings.NewReader(`{"manifest":{"project":"shop"}}`))
		req.AddCookie(cookie)
		for i := 0; i < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode == 403 && strings.Contains(string(body), "another site")
	}
	// An app on the box posting with the cookie (no Content-Type: no preflight).
	if !refused("POST", "Sec-Fetch-Site", "same-site", "Origin", "https://shop.box.example") {
		t.Fatal("a same-site app's request was let through")
	}
	if !refused("POST", "Origin", "https://shop.box.example") {
		t.Fatal("another origin without Sec-Fetch-Site was let through")
	}
	host := strings.TrimPrefix(e.srv.URL, "http://")
	if refused("POST", "Sec-Fetch-Site", "same-origin") || refused("POST", "Origin", "http://"+host) || refused("POST") {
		t.Fatal("the dashboard's own request was refused")
	}
	if refused("GET", "Sec-Fetch-Site", "cross-site") {
		t.Fatal("a read was refused")
	}
}

// A sibling app can't swap the dashboard's session: the cookie is __Host-
// (no Domain= cookie from another host can shadow it), and signing in with a
// code from another site is refused before the code is spent.
func TestSessionCreateRefusesOtherSites(t *testing.T) {
	e := newEnv(t)
	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Sam", "role": "member"})
	code := strings.SplitN(inv["url"].(string), "#", 2)[1]
	post := func(site string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", e.srv.URL+"/v1/session", strings.NewReader(`{"code":"`+code+`"}`))
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := post("same-site"); res.StatusCode != 403 {
		t.Fatalf("sign-in from a sibling app: %d", res.StatusCode)
	}
	res := post("same-origin")
	if res.StatusCode != 200 {
		t.Fatalf("sign-in from the dashboard: %d", res.StatusCode)
	}
	var c *http.Cookie
	for _, x := range res.Cookies() {
		if x.Name == "__Host-tiffin_session" {
			c = x
		}
	}
	if c == nil || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("session cookie: %+v", res.Cookies())
	}
	// A planted plain tiffin_session cookie is not read.
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/whoami", nil)
	req.AddCookie(&http.Cookie{Name: "tiffin_session", Value: e.owner})
	r2, _ := http.DefaultClient.Do(req)
	r2.Body.Close()
	if r2.StatusCode != 401 {
		t.Fatalf("plain tiffin_session cookie read: %d", r2.StatusCode)
	}
}

// An admin can neither make a sign-in link for the owner nor end the owner's
// sessions through the API key revoke.
func TestAdminCannotActAsOwner(t *testing.T) {
	e := newEnv(t)
	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Ann", "role": "admin"})
	code := strings.SplitN(inv["url"].(string), "#", 2)[1]
	res, _ := http.Post(e.srv.URL+"/v1/session", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	res.Body.Close()
	var ann string
	for _, c := range res.Cookies() {
		if c.Name == api.SessionCookie {
			ann = c.Value
		}
	}
	if c, _, _ := e.call(ann, "POST", "/v1/people/usr_owner/login-link", nil); c != 403 {
		t.Fatalf("admin's link for the owner: %d", c)
	}
	_, l, _ := e.call(e.owner, "POST", "/v1/login-links", nil)
	res, _ = http.Post(e.srv.URL+"/v1/session", "application/json", strings.NewReader(`{"code":"`+l["code"].(string)+`"}`))
	res.Body.Close()
	_, _, sessions := e.call(ann, "GET", "/v1/sessions?person=usr_owner", nil)
	if len(sessions) != 1 {
		t.Fatalf("owner sessions: %v", sessions)
	}
	id := sessions[0].(map[string]any)["id"].(string)
	if c, _, _ := e.call(ann, "DELETE", "/v1/tokens/"+id, nil); c != 403 {
		t.Fatalf("admin revoked the owner's session as a key: %d", c)
	}
	if c, _, _ := e.call(ann, "DELETE", "/v1/sessions/"+id, nil); c != 403 {
		t.Fatalf("admin ended the owner's session: %d", c)
	}
}
