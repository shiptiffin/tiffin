package cli

import (
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
