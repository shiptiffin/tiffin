package srcpack

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// WorkspaceRoot finds the JavaScript workspace (monorepo) an app belongs
// to: the nearest folder above it with a pnpm-workspace.yaml or a
// package.json "workspaces" field, at most six levels up and never above
// the repository's top (a folder with .git), or above limit when it is set.
// The app belongs to it when the workspace lists the app's folder among its
// packages, or the app depends on a sibling package ("workspace:*"). It
// returns that folder and the app's path in it (slash-separated), or ok
// false when the app stands alone. As on Vercel, such an app installs at
// the top of its workspace and builds in its folder.
func WorkspaceRoot(appDir, limit string) (root, rel string, ok bool) {
	app, err := filepath.Abs(appDir)
	if err != nil || !exists(filepath.Join(app, "package.json")) {
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
		globs, isRoot := workspaceGlobs(dir)
		if !isRoot {
			continue
		}
		r, err := filepath.Rel(dir, app)
		if err != nil {
			return "", "", false
		}
		r = filepath.ToSlash(r)
		if member(globs, r) || usesWorkspace(app) {
			return dir, r, true
		}
		return "", "", false
	}
	return "", "", false
}

// member reports whether rel matches the workspace's package globs (a
// "!" glob excludes).
func member(globs []string, rel string) bool {
	in := false
	for _, g := range globs {
		g = strings.TrimSuffix(strings.TrimPrefix(path.Clean(strings.TrimPrefix(g, "./")), "./"), "/")
		if neg := strings.HasPrefix(g, "!"); neg {
			if globMatch(strings.TrimPrefix(g, "!"), rel) {
				in = false
			}
		} else if globMatch(g, rel) {
			in = true
		}
	}
	return in
}

// workspaceGlobs reads a workspace root's package globs; isRoot is false
// when dir is not one.
func workspaceGlobs(dir string) (globs []string, isRoot bool) {
	if raw, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml")); err == nil {
		return yamlPackages(raw), true
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, false
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(raw, &pkg) != nil || len(pkg.Workspaces) == 0 || string(pkg.Workspaces) == "null" {
		return nil, false
	}
	if json.Unmarshal(pkg.Workspaces, &globs) != nil {
		var obj struct {
			Packages []string `json:"packages"`
		}
		_ = json.Unmarshal(pkg.Workspaces, &obj) // Yarn's {"packages": [...]}
		globs = obj.Packages
	}
	return globs, true
}

// yamlPackages reads the "packages:" list of a pnpm-workspace.yaml (block
// or flow style), which is all the box needs from it.
func yamlPackages(raw []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	in := false
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "packages:"):
			rest := strings.TrimSpace(strings.TrimPrefix(line, "packages:"))
			if strings.HasPrefix(rest, "[") {
				for _, g := range strings.Split(strings.Trim(rest, "[]"), ",") {
					if g = unquote(g); g != "" {
						out = append(out, g)
					}
				}
				return out
			}
			in = true
		case !in || trimmed == "" || strings.HasPrefix(trimmed, "#"):
		case strings.HasPrefix(trimmed, "- "):
			out = append(out, unquote(strings.TrimPrefix(trimmed, "- ")))
		case line[0] != ' ' && line[0] != '\t' && line[0] != '-':
			return out // the next key
		}
	}
	return out
}

func unquote(s string) string { return strings.Trim(strings.TrimSpace(s), `"'`) }

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

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
