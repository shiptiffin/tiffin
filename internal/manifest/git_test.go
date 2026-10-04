package manifest

import (
	"strings"
	"testing"
)

func TestGitBlock(t *testing.T) {
	m, err := Parse([]byte(`{"project":"shop","apps":{"web":{"git":{"repo":"acme/shop","path":"./"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := m.Apps["web"].Git
	if g.Branch != "main" || g.Previews != PreviewsSameRepo || g.Path != "" {
		t.Fatalf("defaults: %+v", g)
	}
	if got := string(RenderConfig(m, "")); !strings.Contains(got, `web: { git: { repo: "acme/shop" } }`) {
		t.Fatalf("defaults are left out of the rendered config:\n%s", got)
	}
	for _, p := range []string{"a/../b", "../x", "a//b"} {
		_, err := Parse([]byte(`{"project":"shop","apps":{"web":{"git":{"repo":"acme/shop","path":"` + p + `"}}}}`))
		if err == nil || !strings.Contains(err.Error(), "/apps/web/git/path") {
			t.Errorf("path %q: %v", p, err)
		}
	}
}
