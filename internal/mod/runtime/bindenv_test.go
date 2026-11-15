package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

func TestWithBindHost(t *testing.T) {
	for _, c := range []struct {
		name string
		spec manifest.App
		in   map[string]string
		want string // HOST in the result, "" for none
	}{
		{"bun server", manifest.App{Framework: manifest.FrameworkBun}, nil, bindHost},
		{"hono server", manifest.App{Framework: manifest.FrameworkHono}, map[string]string{"X": "1"}, bindHost},
		{"a framework's own HOST stays", manifest.App{Framework: manifest.FrameworkBun}, map[string]string{"HOST": "127.0.0.1"}, "127.0.0.1"},
		{"next reads HOSTNAME", manifest.App{Framework: manifest.FrameworkNext}, nil, ""},
		{"python", manifest.App{Framework: manifest.FrameworkFastAPI}, nil, ""},
		{"worker", manifest.App{Framework: manifest.FrameworkBun, Role: manifest.RoleWorker}, nil, ""},
	} {
		if got := withBindHost(c.spec, c.in)["HOST"]; got != c.want {
			t.Errorf("%s: HOST %q, want %q", c.name, got, c.want)
		}
	}
}

// A plain Bun server's image gets HOST; the app's own env is applied on top
// when the container runs, so it wins.
func TestBunServerImageBindsEveryInterface(t *testing.T) {
	bin, _ := fakeTools(t)
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, memoryMB: 2048}
	req := buildReq(t, manifest.App{Framework: manifest.FrameworkHono}, map[string]string{
		"package.json": `{"dependencies":{"hono":"4"},"scripts":{"start":"bun run src/index.ts"}}`,
	})
	if _, err := b.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(req.WorkDir, "plan", "railpack-plan.json"))
	var plan struct {
		Deploy struct {
			Variables map[string]string `json:"variables"`
		} `json:"deploy"`
	}
	_ = json.Unmarshal(raw, &plan)
	if plan.Deploy.Variables["HOST"] != bindHost {
		t.Errorf("the image's env %v lacks HOST=%s", plan.Deploy.Variables, bindHost)
	}
}
