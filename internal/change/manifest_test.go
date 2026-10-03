package change

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

// roundTripConfigs are every valid config in the repo: the manifest golden
// cases, the examples, the templates and the embedded starters.
func roundTripConfigs(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	var out []string
	for _, pat := range []string{
		"internal/manifest/testdata/*/tiffin.config.ts",
		"examples/*/tiffin.config.ts",
		"templates/*/tiffin.config.ts",
		"internal/starters/files/*/tiffin.config.ts",
	} {
		m, err := filepath.Glob(filepath.Join(root, pat))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range m {
			if strings.HasPrefix(filepath.Base(filepath.Dir(p)), "err-") {
				continue
			}
			out = append(out, p)
		}
	}
	if len(out) < 15 {
		t.Fatalf("only %d configs found", len(out))
	}
	return out
}

func loadConfig(t *testing.T, path string) (*manifest.Manifest, []byte) {
	t.Helper()
	var env map[string]string
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "env.json")); err == nil {
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatal(err)
		}
	}
	m, canon, err := manifest.Load(path, env)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m, canon
}

// Parse → Resources → ManifestFromResources gives the same canonical JSON.
func TestManifestFromResourcesRoundTrip(t *testing.T) {
	for _, path := range roundTripConfigs(t) {
		name := filepath.Base(filepath.Dir(filepath.Dir(path))) + "/" + filepath.Base(filepath.Dir(path))
		t.Run(name, func(t *testing.T) {
			m, want := loadConfig(t, path)
			res, err := Resources(m)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ManifestFromResources(m.Project, res)
			if err != nil {
				t.Fatal(err)
			}
			canon, err := manifest.Canonical(got)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(canon, want) {
				t.Fatalf("round trip differs\n--- got ---\n%s\n--- want ---\n%s", canon, want)
			}
			// And the rebuilt manifest plans to nothing against the original.
			res2, _ := Resources(got)
			if ops := Diff(res, res2); len(ops) != 0 {
				t.Fatalf("rebuilt manifest plans %d ops: %+v", len(ops), ops)
			}
		})
	}
}

func TestManifestFromResourcesRejectsUnknownKinds(t *testing.T) {
	for addr, spec := range map[string]string{
		"widget/x":      `{}`,
		"service/mongo": `{}`,
		"app/web":       `{"path":".","nope":1}`,
		"env/LOG_LEVEL": `3`,
	} {
		_, err := ManifestFromResources("p", map[string]Resource{addr: {Address: addr, Spec: json.RawMessage(spec)}})
		if err == nil || !strings.Contains(err.Error(), addr) {
			t.Errorf("%s: want an error naming the resource, got %v", addr, err)
		}
	}
	// An empty project is just the project.
	m, err := ManifestFromResources("empty", map[string]Resource{KindProject: {Address: KindProject, Spec: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := manifest.Canonical(m)
	if string(canon) != "{\n  \"version\": 1,\n  \"project\": \"empty\"\n}\n" {
		t.Fatalf("empty project: %s", canon)
	}
}
