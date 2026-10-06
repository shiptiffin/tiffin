package srcpack

import (
	"os"
	"path/filepath"
	"reflect"
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
	write("pnpm/pnpm-workspace.yaml", "packages:\n  - apps/*\n  - 'packages/*'\n  - '!apps/legacy'\nallowBuilds:\n  esbuild: true\n")
	write("pnpm/apps/web/package.json", `{"dependencies": {"next": "16.3.8", "@x/core": "workspace:*"}}`)
	write("pnpm/apps/site/package.json", `{"dependencies": {"next": "16"}}`) // a member without workspace packages
	write("pnpm/apps/legacy/package.json", `{"dependencies": {"hono": "4"}}`)
	write("pnpm/tools/cli/package.json", `{"dependencies": {"x": "1"}}`)
	write("pnpm/tools/linked/package.json", `{"dependencies": {"@x/core": "workspace:^"}}`)
	write("npm/package.json", `{"workspaces": ["apps/*"]}`)
	write("npm/apps/api/package.json", `{"devDependencies": {"@x/lib": "1"}}`)
	write("yarn/package.json", `{"workspaces": {"packages": ["services/**"]}}`)
	write("yarn/services/a/b/package.json", `{}`)
	write("lost/apps/web/package.json", `{"dependencies": {"@x/core": "workspace:*"}}`)
	write("lost/apps/web/.git/HEAD", "ref: main") // the repository's top is the app
	write("alone/site/index.html", "no package.json")

	for _, tc := range []struct {
		app, limit, root, rel string
		ok                    bool
	}{
		{"pnpm/apps/web", "", "pnpm", "apps/web", true},
		{"pnpm/apps/site", "", "pnpm", "apps/site", true},
		{"pnpm/apps/legacy", "", "", "", false},
		{"pnpm/tools/cli", "", "", "", false},
		{"pnpm/tools/linked", "", "pnpm", "tools/linked", true},
		{"npm/apps/api", "", "npm", "apps/api", true},
		{"npm/apps/api", "npm/apps", "", "", false},
		{"yarn/services/a/b", "", "yarn", "services/a/b", true},
		{"lost/apps/web", "", "", "", false},
		{"alone/site", "", "", "", false},
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

func TestYAMLPackages(t *testing.T) {
	for in, want := range map[string][]string{
		"packages:\n  - apps/*\n  - \"packages/*\" # libs\n\nonlyBuiltDependencies:\n  - esbuild\n": {"apps/*", "packages/*"},
		"allowBuilds:\n  sharp: false\npackages:\n- apps/*\n":                                       {"apps/*"},
		"packages: [apps/*, 'libs/*']\n":                                                            {"apps/*", "libs/*"},
		"catalog:\n  react: 19\n":                                                                   nil,
	} {
		if got := yamlPackages([]byte(in)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
