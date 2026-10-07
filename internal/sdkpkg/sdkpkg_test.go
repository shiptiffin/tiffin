package sdkpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	for _, want := range []string{"package/package.json", "package/dist/kv.js", "package/dist/kv.d.ts", "package/dist/client/index.js", "package/dist/next/cache-handler.js", "package/LICENSE", "package/README.md"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("tarball lacks %s", want)
		}
	}
	var pkg struct {
		Name, Version string
		Exports       map[string]any
		Dependencies  map[string]string
	}
	if err := json.Unmarshal(seen["package/package.json"], &pkg); err != nil || pkg.Name != "@shiptiffin/sdk" || pkg.Version != SDK.Version {
		t.Fatalf("package.json: %+v %v", pkg, err)
	}
	for name, v := range pkg.Dependencies {
		if strings.HasPrefix(v, "workspace:") {
			t.Errorf("workspace dependency %s in the vendored package", name)
		}
	}
	if kv, _ := pkg.Exports["./kv"].(map[string]any); kv["default"] != "./dist/kv.js" || kv["types"] != "./dist/kv.d.ts" {
		t.Errorf("exports ./kv: %v", pkg.Exports["./kv"])
	}
	// Relative imports carry .js, so Node's ESM loader resolves them.
	if js := string(seen["package/dist/next/index.js"]); !strings.Contains(js, `from "./store.js"`) {
		t.Errorf("next/index.js imports without extensions:\n%s", js)
	}
	if js := string(seen["package/dist/next/index.js"]); strings.Contains(js, "packages/") {
		t.Error("build paths leaked into the package")
	}
}

// Add writes the tarball, points package.json at it (keeping the file's key
// order), and replaces an older vendored version.
func TestAdd(t *testing.T) {
	dir := t.TempDir()
	if _, err := Add(dir); err != ErrNoPackageJSON {
		t.Fatalf("without package.json: %v", err)
	}
	pj := filepath.Join(dir, "package.json")
	_ = os.WriteFile(pj, []byte(`{"name":"web","private":true,"scripts":{"build":"next build"},"dependencies":{"next":"16.3.8","react":"19.3.0"}}`), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "vendor"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "vendor", "shiptiffin-sdk-0.0.1.tgz"), []byte("old"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "vendor", "shiptiffin-sdk-extra-1.0.0.tgz"), []byte("someone else's"), 0o644)
	res, err := Add(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(pj)
	got := string(raw)
	for _, want := range []string{`"@shiptiffin/sdk": "file:./vendor/` + SDK.File() + `"`} {
		if !strings.Contains(got, want) {
			t.Errorf("package.json lacks %s:\n%s", want, got)
		}
	}
	if strings.Index(got, `"name"`) > strings.Index(got, `"scripts"`) || strings.Index(got, `"next"`) > strings.Index(got, `"@shiptiffin/sdk"`) || !strings.HasPrefix(got, "{\n  \"name\": \"web\"") {
		t.Errorf("key order or indentation changed:\n%s", got)
	}
	for _, f := range res.Files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "shiptiffin-sdk-0.0.1.tgz")); !os.IsNotExist(err) || len(res.Removed) != 1 {
		t.Errorf("the old version should be removed: %v", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "shiptiffin-sdk-extra-1.0.0.tgz")); err != nil {
		t.Error("an unrelated tarball was removed")
	}
	// Again: same bytes, nothing else changes.
	before, _ := os.ReadFile(filepath.Join(dir, "vendor", SDK.File()))
	if _, err := Add(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "vendor", SDK.File()))
	if raw2, _ := os.ReadFile(pj); !bytes.Equal(before, after) || string(raw2) != got {
		t.Error("adding twice changed the tarball or package.json")
	}
}

// An app that installs the SDK from npm keeps it: nothing is vendored.
func TestAddKeepsNPM(t *testing.T) {
	dir := t.TempDir()
	pj := filepath.Join(dir, "package.json")
	in := []byte(`{"name":"web","dependencies":{"@shiptiffin/sdk":"^0.1.0"}}`)
	_ = os.WriteFile(pj, in, 0o644)
	res, err := Add(dir)
	if err != nil || res.FromNPM != "^0.1.0" || len(res.Files) != 0 {
		t.Fatalf("Add: %+v %v", res, err)
	}
	if raw, _ := os.ReadFile(pj); !bytes.Equal(raw, in) {
		t.Errorf("package.json changed: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor")); !os.IsNotExist(err) {
		t.Error("vendor/ was written")
	}
}

// The cache handlers come out as one flat directory of ESM files whose
// relative imports all resolve inside it.
func TestNextCacheHandlers(t *testing.T) {
	hs, err := NextCacheHandlers()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cache-handler.js", "use-cache.js", "store.js", "resp.js"} {
		js, ok := hs[name]
		if !ok {
			t.Fatalf("%s missing", name)
		}
		for _, m := range regexp.MustCompile(`from "(\.{1,2}/[^"]+)"`).FindAllSubmatch(js, -1) {
			if _, ok := hs[strings.TrimPrefix(string(m[1]), "./")]; !ok {
				t.Errorf("%s imports %s, which is not next to it", name, m[1])
			}
		}
	}
}

// The declarations name no Bun type: a Node app that type-checks its
// dependencies (skipLibCheck: false) has no @types/bun to resolve one.
func TestDeclarationsNeedNoBunTypes(t *testing.T) {
	comment := regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)
	bunType := regexp.MustCompile(`\bBun\.[A-Z]`)
	err := fs.WalkDir(files, "files", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !strings.HasSuffix(p, ".d.ts") {
			return err
		}
		raw, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		if m := bunType.Find(comment.ReplaceAll(raw, nil)); m != nil {
			t.Errorf("%s names %s", p, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
