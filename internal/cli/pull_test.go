package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPull(t *testing.T) {
	env := newEnv(t)
	code, out, _ := run(t, env, "plan", "testdata")
	if code != ExitOK {
		t.Fatalf("plan: %d %s", code, out)
	}
	hash := decode(t, out)["hash"].(string)
	if code, out, _ = run(t, env, "apply", "testdata", "--confirm", hash); code != ExitOK {
		t.Fatalf("apply: %d %s", code, out)
	}

	// Pull into an empty directory: the only project is picked.
	dir := filepath.Join(t.TempDir(), "pulled")
	code, out, _ = run(t, env, "pull", dir)
	res := decode(t, out)
	if code != ExitOK || res["written"] != true || res["project"] != "hello" || res["version"].(float64) != 1 {
		t.Fatalf("pull: %d %s", code, out)
	}
	if s := res["summary"].(string); s != "3 apps: api (hono), web (next), worker (bun); services: postgres, valkey, storage; 1 env var" {
		t.Errorf("summary: %q", s)
	}
	cfg := filepath.Join(dir, "tiffin.config.ts")
	src, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`project: "hello"`, `worker: { path: "apps/worker", role: "worker", memoryMB: 256 }`, `uploads: { public: true }`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("pulled config lacks %q:\n%s", want, src)
		}
	}
	// The pulled config plans to nothing: box and files agree.
	code, out, _ = run(t, env, "plan", dir)
	if ops, _ := decode(t, out)["ops"].([]any); code != ExitOK || len(ops) != 0 {
		t.Fatalf("plan of pulled config: %d %s", code, out)
	}
	// Pulling again changes nothing.
	code, out, _ = run(t, env, "pull", dir)
	if res := decode(t, out); code != ExitOK || res["written"] != false || res["changed"] != false {
		t.Fatalf("second pull: %d %s", code, out)
	}

	// A local edit is never overwritten without --force, but the diff shows.
	edited := strings.Replace(string(src), "memoryMB: 256", "memoryMB: 384", 1)
	if err := os.WriteFile(cfg, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = run(t, env, "pull", dir)
	res = decode(t, out)
	if code != ExitInvalid || res["written"] != false || res["linesAdded"].(float64) != 1 || res["linesRemoved"].(float64) != 1 ||
		!strings.Contains(res["diff"].(string), "- "+`    worker: { path: "apps/worker", role: "worker", memoryMB: 384 }`) {
		t.Fatalf("pull over an edit: %d %s", code, out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != edited {
		t.Fatal("the edited config was overwritten without --force")
	}
	code, out, _ = run(t, env, "pull", dir, "--force")
	if res := decode(t, out); code != ExitOK || res["written"] != true {
		t.Fatalf("pull --force: %d %s", code, out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != string(src) {
		t.Fatal("--force did not restore the box's config")
	}

	// With two projects the CLI asks which one, unless the dir has a config.
	m := filepath.Join(t.TempDir(), "blog.json")
	_ = os.WriteFile(m, []byte(`{"project":"blog"}`), 0o644)
	_, out, _ = run(t, env, "plan", m)
	run(t, env, "apply", m, "--confirm", decode(t, out)["hash"].(string))
	if code, out, _ = run(t, env, "pull", t.TempDir()); code != ExitInvalid || !strings.Contains(string(out), "--project") {
		t.Fatalf("ambiguous pull: %d %s", code, out)
	}
	if code, out, _ = run(t, env, "pull", dir); code != ExitOK {
		t.Fatalf("pull with the project from the existing config: %d %s", code, out)
	}
	jdir := t.TempDir()
	if code, out, _ = run(t, env, "pull", jdir, "--project", "blog"); code != ExitOK {
		t.Fatalf("pull --project: %d %s", code, out)
	}
	if code, out, _ = run(t, env, "pull", t.TempDir(), "--project", "nope"); code != ExitError || !strings.Contains(string(out), "not_found") {
		t.Fatalf("unknown project: %d %s", code, out)
	}
}

func TestLineDiff(t *testing.T) {
	d, add, rm := lineDiff("a\nb\nc\nd\ne\nf\n", "a\nb\nX\nd\ne\nf\ng\n")
	want := "  b\n- c\n+ X\n  d\n@@\n  f\n+ g\n"
	if d != want || add != 2 || rm != 1 {
		t.Fatalf("diff %q (+%d -%d), want %q", d, add, rm, want)
	}
	if d, _, _ := lineDiff("same\n", "same\n"); d != "" {
		t.Fatalf("no change: %q", d)
	}
}

// An app whose live version is a starter gets the starter's source next to the
// pulled config; files already there are kept, and other apps get nothing.
func TestPullStarterSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/projects/notes/manifest":
			fmt.Fprint(w, `{"project":"notes","version":2,"config":"export default {}\n","manifest":{"project":"notes",`+
				`"apps":{"web":{"framework":"next","path":"."},"api":{"framework":"hono","path":"api"},"bad":{"framework":"hono","path":"../out"}}}}`)
		case "/v1/projects/notes/apps/web/deploys":
			fmt.Fprint(w, `{"deploys":[{"status":"failed","source":"upload"},{"status":"live","source":"template","template":"next-postgres"}]}`)
		case "/v1/projects/notes/apps/api/deploys":
			fmt.Fprint(w, `{"deploys":[{"status":"live","source":"upload"},{"status":"superseded","source":"template","template":"hono-postgres"}]}`)
		case "/v1/projects/notes/apps/bad/deploys":
			fmt.Fprint(w, `{"deploys":[{"status":"live","source":"template","template":"hono-postgres"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	env := map[string]string{"TIFFIN_URL": srv.URL, "TIFFIN_TOKEN": "tfn_x", "TIFFIN_HOME": filepath.Join(t.TempDir(), "unused")}

	dir := t.TempDir()
	mine := filepath.Join(dir, "app", "page.jsx")
	_ = os.MkdirAll(filepath.Dir(mine), 0o755)
	_ = os.WriteFile(mine, []byte("mine"), 0o644)

	code, out, _ := run(t, env, "pull", dir, "--project", "notes")
	if code != ExitOK {
		t.Fatalf("pull: %d %s", code, out)
	}
	var res pullResult
	if err := json.Unmarshal(out, &res); err != nil || len(res.Sources) != 1 {
		t.Fatalf("sources: %s", out)
	}
	s := res.Sources[0]
	if s.App != "web" || s.Starter != "next-postgres" || s.Dir != dir || s.Error != "" || len(s.Kept) != 1 || s.Kept[0] != "app/page.jsx" {
		t.Fatalf("source: %+v", s)
	}
	for _, f := range []string{"package.json", "app/layout.jsx", "lib/db.js"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(mine); string(b) != "mine" {
		t.Error("an existing file was overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tiffin.config.ts")); string(b) != "export default {}\n" {
		t.Errorf("the starter's own config replaced the project's: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "api")); !os.IsNotExist(err) {
		t.Error("an app not running a starter got source")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "out")); !os.IsNotExist(err) {
		t.Error("an app path outside the folder was written")
	}
}
