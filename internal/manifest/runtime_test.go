package manifest

import (
	"strings"
	"testing"
)

func TestRuntime(t *testing.T) {
	m, err := Parse([]byte(`{"project":"shop","apps":{"web":{"framework":"next","runtime":"node"},"api":{"runtime":"bun"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Apps["web"].Runtime != RuntimeNode {
		t.Fatalf("runtime: %q", m.Apps["web"].Runtime)
	}
	got := string(RenderConfig(m, ""))
	if !strings.Contains(got, `runtime: "node"`) || strings.Contains(got, `runtime: "bun"`) {
		t.Fatalf("node is written, the bun default is left out:\n%s", got)
	}
	for fw, want := range map[string]string{"static": "no runtime", "hono": "@hono/node-server"} {
		_, err := Parse([]byte(`{"project":"shop","apps":{"web":{"framework":"` + fw + `","runtime":"node"}}}`))
		if err == nil || !strings.Contains(err.Error(), "/apps/web/runtime") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s on node: %v", fw, err)
		}
	}
	if _, err := Parse([]byte(`{"project":"shop","apps":{"web":{"runtime":"deno"}}}`)); err == nil {
		t.Error("an unknown runtime should be refused")
	}
}
