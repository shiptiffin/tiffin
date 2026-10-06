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

func TestNextCopiesWithoutKV(t *testing.T) {
	warn := func(js string) bool {
		m, err := Parse([]byte(js))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range Warnings(m) {
			if strings.Contains(w, "no KV") {
				return true
			}
		}
		return false
	}
	if !warn(`{"project":"shop","apps":{"web":{"framework":"next","instances":2}}}`) {
		t.Error("two Next.js copies without KV should warn")
	}
	if warn(`{"project":"shop","apps":{"web":{"framework":"next","instances":2}},"services":{"valkey":{}}}`) {
		t.Error("with KV the copies share a cache: no warning")
	}
	if warn(`{"project":"shop","apps":{"web":{"framework":"next"}}}`) {
		t.Error("one copy: no warning")
	}
}
