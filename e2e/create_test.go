//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
)

// gitRepo is a tiny public repository (a static page) for the git-URL deploy.
const gitRepo = "https://github.com/octocat/Spoon-Knife"

// TestCreate is the dashboard's create flow on a fresh box, through the CLI
// (which calls the same API the dashboard does):
//
//	templates list → merge every template's fragment into a new project's
//	manifest → plan/apply → deploy each template → each app answers over
//	HTTPS → GET /manifest round-trips (and tiffin pull plans to nothing) →
//	add an app by editing the fetched manifest → deploy it from a git URL →
//	box resources shows the machine, the services and the app containers.
func TestCreate(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "create", "starter")
	phase("up", start)

	// ---- templates → one manifest → plan/apply ----
	p := time.Now()
	var tl struct {
		Templates []struct {
			ID, Name, Framework, App string
			Services                 []string
			Fragment                 struct {
				Apps     map[string]any `json:"apps"`
				Services map[string]any `json:"services"`
			}
		}
	}
	_, out := b.run("templates", "list")
	if err := json.Unmarshal([]byte(out), &tl); err != nil || len(tl.Templates) != 4 {
		t.Fatalf("templates list: %v\n%s", err, out)
	}
	apps, services := map[string]any{}, map[string]any{}
	for _, tp := range tl.Templates {
		for k, v := range tp.Fragment.Apps {
			apps[k] = v
		}
		for k, v := range tp.Fragment.Services {
			services[k] = v
		}
	}
	doc := map[string]any{"project": "starter", "apps": apps, "services": services}
	raw, _ := json.Marshal(doc)
	b.apply("starter", string(raw))
	b.waitReady("service/postgres", "service/valkey", "service/analytics")
	phase("plan+apply", p)

	// ---- deploy each template; each answers over HTTPS ----
	c := b.https()
	for _, tp := range tl.Templates {
		p = time.Now()
		d := b.ok("deploys", "template", "starter", tp.App, "--template", tp.ID)
		id, _ := d["id"].(string)
		if d["status"] != "queued" || d["template"] != tp.ID || id == "" {
			t.Fatalf("deploy %s: %v", tp.ID, d)
		}
		live := waitDeploy(t, b, "starter", tp.App, id, 10*time.Minute)
		t.Logf("template %-14s → app %-9s build %5.1fs, total %5.1fs, %s", tp.ID, tp.App, live["buildSeconds"], live["durationSeconds"], live["url"])
		phase("deploy "+tp.ID, p)
	}
	checks := []struct{ app, method, path, body, want string }{
		{"site", "GET", "/", "", "It's live."},
		{"api", "GET", "/", "", `"name":"notes"`},
		{"api", "POST", "/notes", `{"text":"from e2e"}`, `"text":"from e2e"`},
		{"api", "GET", "/notes", "", `"text":"from e2e"`},
		{"guestbook", "GET", "/", "", "Sign the book"},
		{"guestbook", "GET", "/", "", "/script.js"}, // analytics is on: the tracker is injected
		{"guestbook", "POST", "/api/entries", `{"name":"e2e","message":"hello box"}`, `"message":"hello box"`},
		{"guestbook", "GET", "/api/entries", "", `"visits":`},
		{"web", "GET", "/", "", "Postgres"},
	}
	for _, ck := range checks {
		var body io.Reader
		if ck.body != "" {
			body = strings.NewReader(ck.body)
		}
		code, _, got := b.get(c, ck.method, b.url(ck.app)+ck.path, body)
		if code >= 300 || !strings.Contains(got, ck.want) {
			t.Fatalf("%s %s%s: %d %s", ck.method, ck.app, ck.path, code, got)
		}
	}
	t.Logf("all four templates answer over HTTPS")

	// ---- the manifest round-trips ----
	p = time.Now()
	pm := b.ok("projects", "manifest", "starter")
	want, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	gotRaw, _ := json.Marshal(pm["manifest"])
	got, err := manifest.Parse(gotRaw)
	if err != nil {
		t.Fatalf("fetched manifest does not parse: %v\n%s", err, gotRaw)
	}
	wc, _ := manifest.Canonical(want)
	gc, _ := manifest.Canonical(got)
	if !bytes.Equal(wc, gc) {
		t.Fatalf("manifest round trip:\n%s\n!=\n%s", gc, wc)
	}
	pull := filepath.Join(b.dir, "pulled")
	if res := b.ok("pull", pull, "--project", "starter"); res["written"] != true {
		t.Fatalf("pull: %v", res)
	}
	if plan := b.ok("plan", pull); len(plan["ops"].([]any)) != 0 {
		t.Fatalf("the pulled config must plan to nothing: %v", plan)
	}
	phase("manifest+pull", p)

	// ---- add an app by editing the fetched manifest, deploy it from git ----
	p = time.Now()
	if out, err := exec.Command("git", "ls-remote", "--heads", gitRepo).CombinedOutput(); err != nil {
		t.Logf("SKIP git-URL deploy: %s is not reachable from this machine (%v: %s)", gitRepo, err, out)
	} else {
		edited := pm["manifest"].(map[string]any)
		edited["apps"].(map[string]any)["spoon"] = map[string]any{"framework": "static"}
		raw2, _ := json.Marshal(edited)
		plan := b.ok("plan", writeJSON(t, b.dir, "edited.json", raw2))
		if ops := plan["ops"].([]any); len(ops) != 1 || ops[0].(map[string]any)["address"] != "app/spoon" {
			t.Fatalf("editing the fetched manifest must plan exactly the new app: %v", plan)
		}
		b.apply("edited", string(raw2))
		d := b.ok("deploys", "git", "starter", "spoon", "--git-url", gitRepo)
		id, _ := d["id"].(string)
		if d["repo"] != gitRepo || d["source"] != "git" {
			t.Fatalf("deploy git: %v", d)
		}
		live := waitDeploy(t, b, "starter", "spoon", id, 5*time.Minute)
		if len(fmt.Sprint(live["commit"])) != 40 {
			t.Fatalf("commit: %v", live)
		}
		log := b.ok("deploys", "build-log", "starter", "spoon", id)
		if text := fmt.Sprint(log["text"]); !strings.Contains(text, "==> cloning "+gitRepo) || !strings.Contains(text, "==> cloned commit") {
			t.Fatalf("build log:\n%s", text)
		}
		if code, _, body := b.get(c, "GET", b.url("spoon")+"/", nil); code != 200 || !strings.Contains(body, "Fork me?") {
			t.Fatalf("spoon: %d %s", code, body)
		}
		t.Logf("git deploy: commit %.12s, total %.1fs", live["commit"], live["durationSeconds"])
	}
	phase("git deploy", p)

	// ---- the box view ----
	p = time.Now()
	r := b.ok("box", "resources")
	cpu := r["cpu"].(map[string]any)
	mem := r["memory"].(map[string]any)
	disks := r["disks"].(map[string]any)
	if cpu["count"].(float64) < 1 || mem["totalBytes"].(float64) < 1<<30 || disks["data"].(map[string]any)["totalBytes"].(float64) == 0 || r["uptimeSeconds"].(float64) <= 0 {
		t.Fatalf("machine: %v", r)
	}
	svc := map[string]map[string]any{}
	for _, s := range r["services"].([]any) {
		m := s.(map[string]any)
		svc[m["name"].(string)] = m
	}
	for _, name := range []string{"tiffin", "postgres", "valkey", "containerd", "buildkit"} {
		if s := svc[name]; s == nil || s["state"] != "active" || (name != "buildkit" && s["memoryBytes"].(float64) == 0) {
			t.Errorf("service %s: %v", name, s)
		}
	}
	running := 0
	for _, a := range r["apps"].([]any) {
		m := a.(map[string]any)
		if m["project"] == "starter" && m["state"] == "running" && m["memoryBytes"].(float64) > 0 {
			running++
		}
	}
	if running < 3 { // api, guestbook, web (static sites run no container)
		t.Errorf("want the 3 container apps running, got %d: %v", running, r["apps"])
	}
	// Timed through the CLI (process start + HTTPS); the sample is cached.
	t0 := time.Now()
	b.ok("box", "resources")
	t.Logf("box: %v CPUs, %.0f MiB memory (%.0f%% used), data disk %.1f GiB, %d services, %d app containers; cached call %s",
		cpu["count"], mem["totalBytes"].(float64)/(1<<20), mem["usedPercent"], disks["data"].(map[string]any)["totalBytes"].(float64)/(1<<30),
		len(svc), len(r["apps"].([]any)), time.Since(t0).Round(time.Millisecond))
	ms := b.inBox(`for i in 1 2 3; do sleep 3.1; curl -s -o /dev/null -w '%{time_total}\n' -H "Authorization: Bearer $(sudo cat /var/lib/tiffin/platform/owner-token)" http://127.0.0.1:7070/v1/box/resources; done`)
	t.Logf("uncached /v1/box/resources in the box (seconds): %s", strings.ReplaceAll(ms, "\n", " "))
	phase("box resources", p)
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}

// waitDeploy polls a deploy until it finishes and fails the test unless it
// went live.
func waitDeploy(t *testing.T, b *cliBox, project, app, id string, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		dep := b.ok("deploys", "get", project, app, id)
		switch dep["status"] {
		case "live":
			return dep
		case "failed":
			log := b.ok("deploys", "build-log", project, app, id)
			t.Fatalf("deploy %s of %s failed: %v\n%s", id, app, dep["error"], log["text"])
		}
		if time.Now().After(deadline) {
			t.Fatalf("deploy %s of %s still %v after %s", id, app, dep["status"], d)
		}
		time.Sleep(2 * time.Second)
	}
}

func writeJSON(t *testing.T, dir, name string, raw []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
