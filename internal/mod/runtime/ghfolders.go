package runtime

import (
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/runtime/ghapp"
)

// maxFolders caps the folder list a repository's detail carries.
const maxFolders = 500

// skippedDir reports whether a folder (and what is below it) is left out of
// detection: build output, dependencies, tests, hidden folders, or deeper
// than 4 levels.
func skippedDir(dir string) bool {
	if dir == "" {
		return false
	}
	segs := strings.Split(dir, "/")
	return len(segs) > 4 || slices.ContainsFunc(segs, func(s string) bool { return skipDirs[s] || strings.HasPrefix(s, ".") })
}

// repoFolders lists a repository's folders for picking an app's folder by
// hand, shallowest first: up to 4 deep, without the folders detection
// skips, at most maxFolders.
func repoFolders(tree []ghapp.TreeEntry) []string {
	seen := map[string]bool{}
	for _, e := range tree {
		dir := e.Path
		if e.Type == "blob" {
			dir = path.Dir(e.Path)
		}
		for d := dir; d != "." && d != "" && !seen[d]; d = path.Dir(d) {
			if !skippedDir(d) {
				seen[d] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if di, dj := depth(out[i]), depth(out[j]); di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	if len(out) > maxFolders {
		out = out[:maxFolders]
	}
	sort.Strings(out) // a tree reads in path order
	return out
}

// dockerRoots are the folders with a Dockerfile that detection found
// nothing else in: the box builds those with their Dockerfile.
func dockerRoots(tree []ghapp.TreeEntry, found []RepoRoot) []RepoRoot {
	taken, apps := map[string]bool{}, map[string]bool{}
	for _, r := range found {
		taken[r.Path] = true
		if !r.Workspace {
			apps[r.Path] = true
		}
	}
	var out []RepoRoot
	for _, e := range tree {
		dir, base := path.Split(e.Path)
		dir = strings.TrimSuffix(dir, "/")
		if e.Type != "blob" || base != manifest.DefaultDockerfile || taken[dir] || skippedDir(dir) || insideApp(dir, apps) {
			continue
		}
		taken[dir] = true
		out = append(out, RepoRoot{Path: dir, Framework: string(manifest.FrameworkBun), Builder: string(manifest.BuilderDockerfile),
			Why: "a Dockerfile and no package.json or Python project: built with the Dockerfile; the app must listen on $PORT"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// insideApp reports whether dir is below one of apps' folders (a Dockerfile
// in an app's own subfolder is not another app). The top ("") counts.
func insideApp(dir string, apps map[string]bool) bool {
	for d := dir; d != "" && d != "."; {
		d = path.Dir(d)
		if d == "." {
			d = ""
		}
		if apps[d] {
			return true
		}
	}
	return false
}
