package api_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

func secretsBox(t *testing.T) (*env, *platform.Secrets, *state.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sec, err := platform.OpenSecrets(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	eng := change.NewEngine(db)
	p := &platform.Platform{DB: db, Engine: eng, Secrets: sec}
	a := api.New(api.Deps{DB: db, Engine: eng, Tokens: tm, Platform: p, Version: "test"})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, owner: owner, tm: tm}, sec, db
}

func TestSecretsCopyStaysInTheBox(t *testing.T) {
	e, sec, _ := secretsBox(t)
	owner := e.owner
	for _, kv := range [][3]string{{"shop", "OPENAI_API_KEY", "sk-1"}, {"shop", "STRIPE_KEY", "rk-2"}, {"blog", "STRIPE_KEY", "blog-own"}} {
		if code, out, _ := e.call(owner, "PUT", "/v1/projects/"+kv[0]+"/secrets/"+kv[1], map[string]any{"value": kv[2]}); code != 200 {
			t.Fatalf("set %v: %d %v", kv, code, out)
		}
	}
	code, out, _ := e.call(owner, "POST", "/v1/projects/blog/secrets/copy", map[string]any{"from": "shop"})
	if code != 200 || len(out["copied"].([]any)) != 1 || out["copied"].([]any)[0] != "OPENAI_API_KEY" || out["skipped"].([]any)[0] != "STRIPE_KEY" {
		t.Fatalf("copy all: %d %v", code, out)
	}
	got, _ := sec.All(t.Context(), "blog")
	if got["OPENAI_API_KEY"] != "sk-1" || got["STRIPE_KEY"] != "blog-own" {
		t.Fatalf("blog secrets: %v", got)
	}
	if code, out, _ = e.call(owner, "POST", "/v1/projects/blog/secrets/copy", map[string]any{"from": "shop", "names": []string{"STRIPE_KEY"}, "overwrite": true}); code != 200 {
		t.Fatalf("overwrite: %d %v", code, out)
	}
	if got, _ = sec.All(t.Context(), "blog"); got["STRIPE_KEY"] != "rk-2" {
		t.Fatalf("overwritten: %v", got)
	}
	if code, _, _ = e.call(owner, "POST", "/v1/projects/blog/secrets/copy", map[string]any{"from": "shop", "names": []string{"NOPE"}}); code != 404 {
		t.Fatalf("unknown name: %d", code)
	}
	// A key that can only reach blog cannot pull shop's secrets into it.
	_, k, _ := e.call(owner, "POST", "/v1/tokens", map[string]any{"name": "blog-only", "projects": []string{"blog"}, "access": "full"})
	if code, _, _ = e.call(k["secret"].(string), "POST", "/v1/projects/blog/secrets/copy", map[string]any{"from": "shop"}); code != 403 {
		t.Fatalf("copy from a project the key can't reach: %d", code)
	}
}

// Setting, replacing, copying and deleting a secret are changes in History
// with the caller's intent; the change log keeps values only encrypted, and
// undo puts back the previous value.
func TestSecretChangesAreUndoable(t *testing.T) {
	e, sec, db := secretsBox(t)
	owner := e.owner
	ctx := t.Context()
	value := func() string {
		all, err := sec.All(ctx, "shop")
		if err != nil {
			t.Fatal(err)
		}
		return all["API_KEY"]
	}
	put := func(v, intent string) string {
		t.Helper()
		code, out, _ := e.call(owner, "PUT", "/v1/projects/shop/secrets/API_KEY", map[string]any{"value": v, "intent": intent})
		if code != 200 {
			t.Fatalf("set: %d %v", code, out)
		}
		id, _ := out["change"].(string)
		return id
	}
	undo := func(id string) {
		t.Helper()
		code, out, _ := e.call(owner, "POST", "/v1/changes/"+id+"/undo", map[string]any{})
		if code != 428 {
			t.Fatalf("undo plan: %d %v", code, out)
		}
		hash := out["plan"].(map[string]any)["hash"].(string)
		if code, out, _ = e.call(owner, "POST", "/v1/changes/"+id+"/undo", map[string]any{"confirm": hash}); code != 200 {
			t.Fatalf("undo: %d %v", code, out)
		}
	}
	first := put("sk-first-plaintext", "add the payments key")
	second := put("sk-second-plaintext", "rotate the payments key")
	if first == "" || second == "" || put("sk-second-plaintext", "") != "" {
		t.Fatalf("set should be a change, and setting the same value none: %q %q", first, second)
	}
	c, err := db.GetChange(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	if c.Intent != "rotate the payments key" || c.Actor.ID == "" || len(c.Plan.Ops) != 1 || c.Plan.Ops[0].Address != "secret/API_KEY" || c.Plan.Risk != change.TierReversible {
		t.Fatalf("change: %s", raw)
	}
	if strings.Contains(string(raw), "plaintext") {
		t.Fatalf("the change log holds a secret in plain text: %s", raw)
	}
	undo(second)
	if v := value(); v != "sk-first-plaintext" {
		t.Fatalf("undo of the rotation left %q", v)
	}
	code, out, _ := e.call(owner, "DELETE", "/v1/projects/shop/secrets/API_KEY?intent=retire", nil)
	if code != 200 || out["change"] == "" {
		t.Fatalf("delete: %d %v", code, out)
	}
	if v := value(); v != "" {
		t.Fatalf("deleted secret still there: %q", v)
	}
	undo(out["change"].(string))
	if v := value(); v != "sk-first-plaintext" {
		t.Fatalf("undo of the delete gave %q", v)
	}
	if code, _, _ = e.call(owner, "DELETE", "/v1/projects/shop/secrets/NOPE", nil); code != 404 {
		t.Fatalf("delete missing: %d", code)
	}

	// A manifest apply keeps secrets (manifests never list them)...
	m := map[string]any{"project": "shop", "env": map[string]any{"LOG": "1"}}
	code, out, _ = e.call(owner, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code != 200 {
		t.Fatalf("plan: %d %v", code, out)
	}
	for _, o := range out["ops"].([]any) {
		if strings.HasPrefix(o.(map[string]any)["address"].(string), "secret/") {
			t.Fatalf("a manifest plan touches secrets: %v", out)
		}
	}
	// ...and destroying the project deletes them, listed in its plan.
	code, out, _ = e.call(owner, "POST", "/v1/projects/shop/destroy", map[string]any{})
	if code != 428 || !strings.Contains(fmt.Sprint(out["plan"]), "secret/API_KEY") {
		t.Fatalf("destroy plan should list the secret: %d %v", code, out)
	}
	// History shows secret changes like any other.
	_, _, list := e.call(owner, "GET", "/v1/changes?project=shop", nil)
	if len(list) < 5 {
		t.Fatalf("history has %d changes: %v", len(list), list)
	}
}

// A plan with nothing to change still names resources that failed to
// converge, and an apply retries them rather than saying all is well.
func TestEmptyApplyRetriesFailed(t *testing.T) {
	e, _, db := secretsBox(t)
	m := map[string]any{"project": "shop", "services": map[string]any{"postgres": map[string]any{}}}
	code, out, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code != 200 {
		t.Fatalf("plan: %d %v", code, out)
	}
	if code, out, _ = e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": out["hash"]}); code != 200 || out["applied"] != true {
		t.Fatalf("apply: %d %v", code, out)
	}
	_ = db.SetResourceStatus(t.Context(), "shop", "service/postgres", platform.StateFailed, "cannot execute CREATE SCHEMA in a read-only transaction\nmore")
	code, out, _ = e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code != 200 || !strings.Contains(fmt.Sprint(out["warnings"]), "service/postgres: cannot execute CREATE SCHEMA in a read-only transaction") {
		t.Fatalf("empty plan with a failure: %d %v", code, out)
	}
	code, out, _ = e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": m})
	if code != 200 || out["applied"] != false || !strings.Contains(fmt.Sprint(out["plan"]), "retrying now") {
		t.Fatalf("empty apply with a failure: %d %v", code, out)
	}
}
