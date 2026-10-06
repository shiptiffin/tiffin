package srcpack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceRoot finds the JavaScript workspace (monorepo) an app needs: when
// the app's package.json depends on a sibling package ("workspace:*", as
// pnpm, Bun and Yarn write it), the nearest folder above it with a
// pnpm-workspace.yaml or a package.json "workspaces" field, at most six
// levels up and never above the repository's top (a folder with .git), or
// above limit when it is set. It returns that folder and the app's path in
// it (slash-separated), or ok false when the app stands alone.
func WorkspaceRoot(appDir, limit string) (root, rel string, ok bool) {
	app, err := filepath.Abs(appDir)
	if err != nil || !usesWorkspace(app) {
		return "", "", false
	}
	if limit != "" {
		if limit, err = filepath.Abs(limit); err != nil {
			return "", "", false
		}
	}
	dir := app
	for range 6 {
		if exists(filepath.Join(dir, ".git")) || dir == limit {
			return "", "", false
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", "", false
		}
		dir = up
		if isWorkspace(dir) {
			r, err := filepath.Rel(dir, app)
			if err != nil {
				return "", "", false
			}
			return dir, filepath.ToSlash(r), true
		}
	}
	return "", "", false
}

func usesWorkspace(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg map[string]json.RawMessage
	if json.Unmarshal(raw, &pkg) != nil {
		return false
	}
	for _, k := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		var deps map[string]string
		_ = json.Unmarshal(pkg[k], &deps)
		for _, v := range deps {
			if strings.HasPrefix(v, "workspace:") {
				return true
			}
		}
	}
	return false
}

func isWorkspace(dir string) bool {
	if exists(filepath.Join(dir, "pnpm-workspace.yaml")) {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	return json.Unmarshal(raw, &pkg) == nil && len(pkg.Workspaces) > 0 && string(pkg.Workspaces) != "null"
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
