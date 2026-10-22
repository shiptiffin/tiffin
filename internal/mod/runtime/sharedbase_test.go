package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// railpackPlan is the shape of the plan Railpack 0.40.1 writes for a
// Next.js app on Bun (trimmed).
const railpackPlan = `{
  "caches": {"apt": {"directory": "/var/cache/apt", "type": "locked"}},
  "deploy": {
    "base": {"step": "packages:apt:runtime"},
    "inputs": [
      {"include": ["/mise/shims", "/mise/installs", "/usr/local/bin/mise", "/etc/mise/config.toml", "/root/.local/state/mise"], "step": "packages:mise"},
      {"include": ["/app/node_modules"], "step": "build"},
      {"exclude": ["node_modules", ".yarn"], "include": ["/root/.cache", "."], "step": "build"}
    ],
    "startCommand": "exec bun --bun ./node_modules/next/dist/bin/next start",
    "variables": {"NODE_ENV": "production"}
  },
  "steps": [
    {"name": "packages:mise", "assets": {"generated-mise-toml": "[tools]\n  bun = \"1.4.2\"\n"}, "commands": [{"cmd": "mise install"}], "inputs": [{"image": "ghcr.io/railwayapp/railpack-builder:mise-2026.9.15"}]},
    {"name": "install", "commands": [{"cmd": "bun install --frozen-lockfile"}], "inputs": [{"step": "packages:mise"}]},
    {"name": "build", "commands": [{"cmd": "bun run build"}], "inputs": [{"step": "install"}, {"include": ["."], "local": true}]},
    {"name": "packages:apt:runtime", "caches": ["apt"], "commands": [{"cmd": "apt-get install -y libatomic1"}], "inputs": [{"image": "ghcr.io/railwayapp/railpack-runtime:mise-2026.9.15"}]}
  ]
}`

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func parsePlan(t *testing.T, raw string) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSplitBase(t *testing.T) {
	s := splitBase(parsePlan(t, railpackPlan), t.TempDir())
	if s == nil {
		t.Fatal("no base")
	}
	raw, _ := json.Marshal(s.plan)
	for _, want := range []string{`"packages:mise"`, `"packages:apt:runtime"`, `"/mise/installs"`, `"caches"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("base plan lacks %s: %s", want, raw)
		}
	}
	for _, not := range []string{`"build"`, `"install"`, `"/app/node_modules"`, "startCommand"} {
		if strings.Contains(string(raw), not) {
			t.Errorf("base plan has %s: %s", not, raw)
		}
	}
	app := s.withBase("sha256:" + strings.Repeat("a", 64))
	d := app["deploy"].(map[string]any)
	if base := d["base"].(map[string]any)["image"]; base != s.ref()+"@sha256:"+strings.Repeat("a", 64) {
		t.Errorf("app base: %v", base)
	}
	if ins := d["inputs"].([]any); len(ins) != 2 || ins[0].(map[string]any)["step"] != "build" {
		t.Errorf("app inputs: %v", ins)
	}
	if d["startCommand"] == nil || len(app["steps"].([]any)) != 4 {
		t.Errorf("app plan lost its own parts: %v", app)
	}

	// The same plan, the same keys; another toolchain, another base; a
	// change above the base, the same base and another verdict.
	if again := splitBase(parsePlan(t, railpackPlan), t.TempDir()); again.key != s.key || again.verdictKey != s.verdictKey {
		t.Error("keys are not stable")
	}
	node := splitBase(parsePlan(t, strings.Replace(railpackPlan, `bun = \"1.4.2\"`, `node = \"24.22.1\"`, 1)), "")
	if node.key == s.key {
		t.Error("another toolchain, the same base")
	}
	env := splitBase(parsePlan(t, strings.Replace(railpackPlan, `"NODE_ENV": "production"`, `"NODE_ENV": "staging"`, 1)), "")
	if env.key != s.key || env.verdictKey == s.verdictKey {
		t.Error("a change in the deploy stage: the base changed or the verdict did not")
	}
	// A step under the base that reads the source: nothing to share.
	if splitBase(parsePlan(t, strings.Replace(railpackPlan, `"inputs": [{"image": "ghcr.io/railwayapp/railpack-runtime`, `"inputs": [{"local": true}, {"image": "ghcr.io/railwayapp/railpack-runtime`, 1)), "") != nil {
		t.Error("a base that reads the source")
	}
	// A step under the base that copies a file of the source: the file is
	// part of the key (Python's mise step copies .python-version).
	pyv := strings.Replace(railpackPlan, `"commands": [{"cmd": "mise install"}]`, `"commands": [{"src": ".python-version", "dest": ".python-version"}, {"cmd": "mise install"}]`, 1)
	src := t.TempDir()
	if splitBase(parsePlan(t, pyv), src) != nil {
		t.Error("a base that copies a file the source lacks")
	}
	writeFiles(t, src, map[string]string{".python-version": "3.14"})
	v1 := splitBase(parsePlan(t, pyv), src)
	writeFiles(t, src, map[string]string{".python-version": "3.13"})
	v2 := splitBase(parsePlan(t, pyv), src)
	if v1 == nil || v2 == nil || v1.key == v2.key || len(v1.files) != 1 {
		t.Errorf("copied files and the key: %v %v", v1, v2)
	}
	if splitBase(parsePlan(t, strings.Replace(pyv, `"src": ".python-version"`, `"src": "."`, 1)), src) != nil {
		t.Error("a base that copies the whole source")
	}
	// Python's plans put the runtime image itself under the copies.
	py := strings.Replace(railpackPlan, `"base": {"step": "packages:apt:runtime"}`, `"base": {"image": "ghcr.io/railwayapp/railpack-runtime:mise-2026.9.15"}`, 1)
	if ps := splitBase(parsePlan(t, py), ""); ps == nil || ps.key == s.key || strings.Contains(mustJSON(ps.plan), "packages:apt:runtime") {
		t.Errorf("an image base: %v", ps)
	}
	if splitBase(parsePlan(t, `{"deploy":{"base":{"image":"x"},"inputs":[{"step":"build","include":["."]}]},"steps":[{"name":"build","inputs":[{"local":true}]}]}`), "") != nil {
		t.Error("a base with no copies to share")
	}
	if splitBase(parsePlan(t, `{"deploy":{}}`), "") != nil {
		t.Error("a plan with no deploy base")
	}
}

// gateEngine is the fake engine plus images named by what gate tests set.
type gateEngine struct {
	*fakeEngine
	tags   [][2]string
	remove []string
}

func (e *gateEngine) TagImage(ctx context.Context, src, ref string) error {
	e.tags = append(e.tags, [2]string{src, ref})
	return nil
}

func (e *gateEngine) RemoveImage(ctx context.Context, ref string) error {
	e.remove = append(e.remove, ref)
	return nil
}

func (e *gateEngine) ImageDigest(ctx context.Context, ref string) (string, error) {
	if strings.HasPrefix(ref, baseRepo) {
		return "sha256:" + strings.Repeat("b", 64), nil // the base is in the store
	}
	return "sha256:" + strings.Repeat("d", 64), nil
}

func TestOnSharedBase(t *testing.T) {
	work := t.TempDir()
	planPath := filepath.Join(work, "plan", "railpack-plan.json")
	writeFiles(t, filepath.Dir(planPath), map[string]string{"railpack-plan.json": railpackPlan})
	legacyTree, sameTree, otherTree := t.TempDir(), t.TempDir(), t.TempDir()
	for _, d := range []string{legacyTree, sameTree} {
		writeFiles(t, d, map[string]string{"app/index.js": "1"})
	}
	writeFiles(t, otherTree, map[string]string{"app/index.js": "2"})
	const ref = "docker.io/tiffin/shop-web:dep_1"

	run := func(b *boxBuilder, eng *gateEngine, candTree string, candErr error) (BuildResult, string, int, int) {
		legacyRuns, candRuns := 0, 0
		var log bytes.Buffer
		req := BuildRequest{Deploy: &Deploy{ID: "dep_1", Project: "shop", App: "web"}, WorkDir: work, Log: &log}
		res, err := b.onSharedBase(context.Background(), req, planPath, ref,
			func() (BuildResult, error) {
				legacyRuns++
				eng.trees[dockerName(ref)] = legacyTree
				return BuildResult{Image: ref, Digest: "sha256:legacy"}, nil
			},
			func(dir, name string, _ io.Writer) (string, string, error) {
				candRuns++
				if candErr != nil {
					return "", candErr.Error(), candErr
				}
				if _, err := os.Stat(filepath.Join(dir, "railpack-plan.json")); err != nil {
					t.Errorf("no plan for the build on the base: %v", err)
				}
				eng.trees[dockerName(name)] = candTree
				return "sha256:onbase", "", nil
			}, func(first string, err error) error { return &BuildError{Msg: first} })
		if err != nil {
			res.Image = "error: " + err.Error()
		}
		return res, log.String(), legacyRuns, candRuns
	}
	newEngine := func() *gateEngine {
		e := &gateEngine{fakeEngine: newFakeEngine()}
		e.trees = map[string]string{}
		return e
	}

	// First build: both ways, the same files: the image on the base ships
	// under the deploy's name, and the verdict is kept.
	b := &boxBuilder{gateDir: t.TempDir()}
	eng := newEngine()
	b.eng = eng
	res, log, l, c := run(b, eng, sameTree, nil)
	if res.Base == "" || res.Digest != "sha256:onbase" || l != 1 || c != 1 || len(eng.tags) != 1 || eng.tags[0] != [2]string{ref + "-shared", ref} {
		t.Fatalf("first build: %+v legacy %d candidate %d tags %v\n%s", res, l, c, eng.tags, log)
	}
	// Then: built once, on the base.
	res, log, l, c = run(b, eng, sameTree, nil)
	if res.Base == "" || l != 0 || c != 1 {
		t.Fatalf("checked build: %+v legacy %d candidate %d\n%s", res, l, c, log)
	}
	// The base can't be found: built as before.
	res, log, l, _ = run(b, eng, sameTree, errors.New(`docker-image://`+baseRepo+`:x@sha256:y: not found`))
	if res.Base != "" || l != 1 || !strings.Contains(log, "the build on it failed") {
		t.Fatalf("failed on the base: %+v legacy %d\n%s", res, l, log)
	}
	// The app's own build fails: that is the build's error, without a
	// second try that would fail the same way.
	res, _, l, _ = run(b, eng, sameTree, errors.New("next build: type error"))
	if res.Image != "error: next build: type error" || l != 0 {
		t.Fatalf("an app's failing build: %+v legacy %d", res, l)
	}

	// Different files: today's image ships, and keeps shipping without
	// another check.
	b2 := &boxBuilder{gateDir: t.TempDir()}
	eng2 := newEngine()
	b2.eng = eng2
	res, log, l, c = run(b2, eng2, otherTree, nil)
	if res.Base != "" || res.Digest != "sha256:legacy" || l != 1 || c != 1 || len(eng2.tags) != 0 || !strings.Contains(log, "app/index.js: content differ") {
		t.Fatalf("different files: %+v legacy %d candidate %d\n%s", res, l, c, log)
	}
	if len(eng2.remove) != 1 || eng2.remove[0] != ref+"-shared" {
		t.Errorf("the image on the base stayed: %v", eng2.remove)
	}
	res, log, l, c = run(b2, eng2, otherTree, nil)
	if res.Base != "" || l != 1 || c != 0 || !strings.Contains(log, "its files differed when the box checked") {
		t.Fatalf("after a mismatch: %+v legacy %d candidate %d\n%s", res, l, c, log)
	}
	// A week later it is compared again.
	s := splitBase(parsePlan(t, railpackPlan), "")
	b2.keepVerdict(s.verdictKey, gateVerdict{Why: "x", At: time.Now().Add(-recheckAfter - time.Hour)})
	if _, _, _, c = run(b2, eng2, sameTree, nil); c != 1 {
		t.Errorf("an old mismatch was not checked again")
	}
}
