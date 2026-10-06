package sdkpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tarball is npm-shaped (package/...), carries built JS and types (no
// sources, no workspace deps) and is byte-for-byte reproducible.
func TestTarball(t *testing.T) {
	a, err := SDK.Tarball()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SDK.Tarball()
	if !bytes.Equal(a, b) {
		t.Fatal("tarball is not reproducible")
	}
	zr, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	seen := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		seen[h.Name] = body
	}
	for _, want := range []string{"package/package.json", "package/lib/kv.js", "package/lib/kv.d.ts", "package/lib/next/cache-handler.js", "package/LICENSE"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("tarball lacks %s", want)
		}
	}
	var pkg struct {
		Name, Version string
		Exports       map[string]any
		Dependencies  map[string]string
	}
	if err := json.Unmarshal(seen["package/package.json"], &pkg); err != nil || pkg.Name != "tiffin-sdk" || pkg.Version != SDK.Version {
		t.Fatalf("package.json: %+v %v", pkg, err)
	}
	for name, v := range pkg.Dependencies {
		if strings.HasPrefix(v, "workspace:") {
			t.Errorf("workspace dependency %s in the vendored package", name)
		}
	}
	if kv, _ := pkg.Exports["./kv"].(map[string]any); kv["default"] != "./lib/kv.js" || kv["types"] != "./lib/kv.d.ts" {
		t.Errorf("exports ./kv: %v", pkg.Exports["./kv"])
	}
	// Relative imports carry .js, so Node's ESM loader resolves them.
	if js := string(seen["package/lib/next/index.js"]); !strings.Contains(js, `from "./store.js"`) {
		t.Errorf("next/index.js imports without extensions:\n%s", js)
	}
	if js := string(seen["package/lib/next/index.js"]); strings.Contains(js, "packages/") {
		t.Error("build paths leaked into the package")
	}
}

// Add writes the tarball, points package.json at it (keeping the file's key
// order), and replaces an older vendored version.
func TestAdd(t *testing.T) {
	dir := t.TempDir()
	if _, err := Add(dir, SDK); err != ErrNoPackageJSON {
		t.Fatalf("without package.json: %v", err)
	}
	pj := filepath.Join(dir, "package.json")
	_ = os.WriteFile(pj, []byte(`{"name":"web","private":true,"scripts":{"build":"next build"},"dependencies":{"next":"16.3.8","react":"19.3.0"}}`), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "vendor"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "vendor", "tiffin-sdk-0.0.1.tgz"), []byte("old"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "vendor", "tiffin-sdk-extra-1.0.0.tgz"), []byte("someone else's"), 0o644)
	res, err := Add(dir, SDK, React)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(pj)
	got := string(raw)
	for _, want := range []string{`"tiffin-sdk": "file:./vendor/` + SDK.File() + `"`, `"@tiffin/react": "file:./vendor/` + React.File() + `"`} {
		if !strings.Contains(got, want) {
			t.Errorf("package.json lacks %s:\n%s", want, got)
		}
	}
	if strings.Index(got, `"name"`) > strings.Index(got, `"scripts"`) || strings.Index(got, `"next"`) > strings.Index(got, `"tiffin-sdk"`) || !strings.HasPrefix(got, "{\n  \"name\": \"web\"") {
		t.Errorf("key order or indentation changed:\n%s", got)
	}
	for _, f := range res.Files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "tiffin-sdk-0.0.1.tgz")); !os.IsNotExist(err) || len(res.Removed) != 1 {
		t.Errorf("the old version should be removed: %v", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "tiffin-sdk-extra-1.0.0.tgz")); err != nil {
		t.Error("an unrelated tarball was removed")
	}
	// Again: same bytes, nothing else changes.
	before, _ := os.ReadFile(filepath.Join(dir, "vendor", SDK.File()))
	if _, err := Add(dir, SDK); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "vendor", SDK.File()))
	if raw2, _ := os.ReadFile(pj); !bytes.Equal(before, after) || string(raw2) != got {
		t.Error("adding twice changed the tarball or package.json")
	}
}
