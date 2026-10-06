package srcpack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceRoot(t *testing.T) {
	top := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(top, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pnpm/.git/HEAD", "ref: main")
	write("pnpm/pnpm-workspace.yaml", "packages: [apps/*, packages/*]")
	write("pnpm/apps/web/package.json", `{"dependencies": {"next": "16.3.8", "@x/core": "workspace:*"}}`)
	write("pnpm/apps/alone/package.json", `{"dependencies": {"hono": "4"}}`)
	write("npm/package.json", `{"workspaces": ["apps/*"]}`)
	write("npm/apps/api/package.json", `{"devDependencies": {"@x/lib": "workspace:^"}}`)
	write("lost/apps/web/package.json", `{"dependencies": {"@x/core": "workspace:*"}}`)
	write("lost/apps/web/.git/HEAD", "ref: main") // the repository's top is the app

	for _, tc := range []struct {
		app, limit, root, rel string
		ok                    bool
	}{
		{"pnpm/apps/web", "", "pnpm", "apps/web", true},
		{"pnpm/apps/alone", "", "", "", false},
		{"npm/apps/api", "", "npm", "apps/api", true},
		{"npm/apps/api", "npm/apps", "", "", false},
		{"lost/apps/web", "", "", "", false},
	} {
		limit := ""
		if tc.limit != "" {
			limit = filepath.Join(top, tc.limit)
		}
		root, rel, ok := WorkspaceRoot(filepath.Join(top, tc.app), limit)
		want := ""
		if tc.ok {
			want = filepath.Join(top, tc.root)
		}
		if ok != tc.ok || root != want || rel != tc.rel {
			t.Errorf("%s: %q %q %v; want %q %q %v", tc.app, root, rel, ok, want, tc.rel, tc.ok)
		}
	}
}
