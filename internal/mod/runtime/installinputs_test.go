package runtime

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A plan as Railpack writes it for an app with a file: dependency: the
// install step takes the whole source.
const wholeSourcePlan = `{"steps":[
 {"name":"packages:mise","inputs":[{"image":"builder"}]},
 {"name":"install","inputs":[{"step":"packages:mise"},{"local":true,"include":["."]}],
  "commands":[{"path":"/app/node_modules/.bin"},{"cmd":"mkdir -p /app/node_modules/.cache"},{"cmd":"bun install --frozen-lockfile"}],
  "secrets":["NODE_OPTIONS"],"caches":["bun-install"]},
 {"name":"build","inputs":[{"step":"install"},{"local":true,"include":["."]}],"commands":[{"src":".","dest":"."},{"cmd":"bun run build"}]}
]}`

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func narrowed(t *testing.T, src string, env map[string]string) (bool, map[string]any) {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "railpack-plan.json")
	if err := os.WriteFile(planPath, []byte(wholeSourcePlan), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := narrowInstall(planPath, src, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(planPath)
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	for _, s := range plan["steps"].([]any) {
		if step := s.(map[string]any); step["name"] == "install" {
			return changed, step
		}
	}
	t.Fatal("no install step")
	return false, nil
}

func TestNarrowInstallFileDependency(t *testing.T) {
	src := writeTree(t, map[string]string{
		"package.json":                `{"scripts":{"build":"next build"},"dependencies":{"next":"16.3.8","@shiptiffin/sdk":"file:./vendor/sdk-0.2.0.tgz"}}`,
		"bun.lock":                    "{}",
		".npmrc":                      "",
		"vendor/sdk-0.2.0.tgz":        "tgz",
		"app/page.tsx":                "export default function Page() {}",
		"node_modules/x/package.json": `{"scripts":{"postinstall":"node evil.js"}}`,
	})
	changed, install := narrowed(t, src, nil)
	if !changed {
		t.Fatal("the install step kept the whole source")
	}
	for _, in := range install["inputs"].([]any) {
		if in.(map[string]any)["local"] == true {
			t.Fatalf("still a local input: %v", install["inputs"])
		}
	}
	var copied, order []string
	for _, c := range install["commands"].([]any) {
		m := c.(map[string]any)
		if s, ok := m["src"].(string); ok {
			copied = append(copied, s)
			order = append(order, "copy")
		} else if cmd, ok := m["cmd"].(string); ok {
			order = append(order, cmd)
		}
	}
	want := []string{".npmrc", "bun.lock", "package.json", "vendor/sdk-0.2.0.tgz"}
	if !slices.Equal(copied, want) {
		t.Fatalf("copied %v, want %v", copied, want)
	}
	// After mkdir, before the install, as Railpack orders them.
	if order[0] != "mkdir -p /app/node_modules/.cache" || order[len(order)-1] != "bun install --frozen-lockfile" {
		t.Fatalf("order %v", order)
	}
	if !slices.Equal(install["secrets"].([]any), []any{"NODE_OPTIONS"}) {
		t.Fatalf("secrets changed: %v", install["secrets"])
	}
}

func TestNarrowInstallLeavesTheWholeSource(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "lib.tgz"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]string{
		"postinstall": {
			"package.json": `{"scripts":{"postinstall":"patch-package"},"dependencies":{"a":"file:./a.tgz"}}`, "a.tgz": "x",
		},
		"prepare in a workspace package": {
			"package.json": `{"dependencies":{"a":"file:./a.tgz"}}`, "a.tgz": "x",
			"packages/ui/package.json": `{"scripts":{"prepare":"tsc"}}`,
		},
		"missing target":      {"package.json": `{"dependencies":{"a":"file:./gone.tgz"}}`},
		"target outside":      {"package.json": `{"dependencies":{"a":"file:` + filepath.Join(outside, "lib.tgz") + `"}}`},
		"broken package.json": {"package.json": `{"dependencies":`},
		"no file: dependency": {"package.json": `{"dependencies":{"next":"16.3.8"}}`},
	}
	for name, files := range cases {
		changed, install := narrowed(t, writeTree(t, files), nil)
		if changed || len(install["inputs"].([]any)) != 2 {
			t.Errorf("%s: narrowed the install step (%v)", name, install["inputs"])
		}
	}
	src := writeTree(t, map[string]string{"package.json": `{"dependencies":{"a":"file:./a.tgz"}}`, "a.tgz": "x"})
	if changed, _ := narrowed(t, src, map[string]string{"NODE_INSTALL_PATTERNS": "scripts/*.js"}); changed {
		t.Error("NODE_INSTALL_PATTERNS: narrowed the install step")
	}
}

func TestNarrowInstallFolderDependency(t *testing.T) {
	src := writeTree(t, map[string]string{
		"package.json":             `{"devDependencies":{"ui":"file:packages/ui"}}`,
		"bun.lock":                 "{}",
		"packages/ui/package.json": `{"name":"ui"}`,
		"packages/ui/index.ts":     "export {}",
		"patches/next.patch":       "diff",
	})
	changed, install := narrowed(t, src, nil)
	if !changed {
		t.Fatal("not narrowed")
	}
	var copied []string
	for _, c := range install["commands"].([]any) {
		if s, ok := c.(map[string]any)["src"].(string); ok {
			copied = append(copied, s)
		}
	}
	want := []string{"bun.lock", "package.json", "packages/ui/package.json", "patches", "packages/ui"}
	if !slices.Equal(copied, want) {
		t.Fatalf("copied %v, want %v", copied, want)
	}
}
