package api_test

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

func TestSecretsCopyStaysInTheBox(t *testing.T) {
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
	p := &platform.Platform{DB: db, Secrets: sec}
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Platform: p, Version: "test"})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	e := &env{t: t, srv: srv, owner: owner, tm: tm}

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
