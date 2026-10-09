package runtime

import (
	"path"
	"regexp"
	"strings"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// Python apps in a repository: a folder with a pyproject.toml, a
// requirements.txt or a Pipfile (what Railpack's Python provider builds
// from). Their dependencies come from those files and uv.lock.

// pyMarkers make a folder a Python app.
var pyMarkers = map[string]bool{"pyproject.toml": true, "requirements.txt": true, "Pipfile": true}

// pyDepFile reports whether a file in a Python app's folder lists its
// dependencies: the markers, uv.lock, and requirements-*.txt variants.
func pyDepFile(base string) bool {
	return pyMarkers[base] || base == "uv.lock" ||
		(strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"))
}

// pyFiles collects, per folder, the Python files detectRoots reads.
type pyFiles map[string][]string

func (p pyFiles) add(dir, base string) {
	if pyDepFile(base) {
		p[dir] = append(p[dir], base)
	}
}

// app reports whether dir is a Python app (it has a marker file).
func (p pyFiles) app(dir string) bool {
	for _, f := range p[dir] {
		if pyMarkers[f] {
			return true
		}
	}
	return false
}

// dirs is the set of Python app folders.
func (p pyFiles) dirs() map[string]bool {
	out := map[string]bool{}
	for d := range p {
		if p.app(d) {
			out[d] = true
		}
	}
	return out
}

// pyReadOrder is the order dependency files are searched: the project's own
// list first, the lockfile last.
func pyReadOrder(files []string) []string {
	rank := func(f string) int {
		switch {
		case f == "pyproject.toml":
			return 0
		case f == "requirements.txt":
			return 1
		case f == "Pipfile":
			return 3
		case f == "uv.lock":
			return 4
		}
		return 2 // requirements-*.txt
	}
	out := append([]string(nil), files...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (rank(out[j]) < rank(out[j-1]) || (rank(out[j]) == rank(out[j-1]) && out[j] < out[j-1])); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// pyName matches a distribution name the way PEP 503 compares them: case
// does not matter and -, _ and . are the same.
func pyName(name string) string {
	parts := pySeps.Split(strings.ToLower(name), -1)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(parts, `[-_.]+`)
}

var pySeps = regexp.MustCompile(`[-_.]+`)

// pyDepRe finds a dependency named name in a file of the given kind.
func pyDepRe(kind, name string) *regexp.Regexp {
	n := pyName(name)
	switch kind {
	case "uv.lock":
		return regexp.MustCompile(`(?im)^name = "` + n + `"\s*$`)
	case "Pipfile":
		return regexp.MustCompile(`(?im)^\s*"?` + n + `"?\s*=`)
	case "pyproject.toml":
		// PEP 621 / PEP 735 strings ("fastapi[standard]>=0.118") or a
		// Poetry table key (fastapi = "^0.118").
		return regexp.MustCompile(`(?im)["']\s*` + n + `\s*(?:\[[^\]]*\])?\s*(?:[=<>~!;@ (]|["'])|^\s*` + n + `\s*=`)
	}
	return regexp.MustCompile(`(?im)^\s*` + n + `\s*(?:\[[^\]]*\])?\s*(?:[=<>~!;@ ]|$)`) // requirements
}

// pyDep reports which of an app's files names dep ("" for none).
func pyDep(contents map[string][]byte, order []string, dep string) string {
	for _, f := range order {
		kind := f
		if kind != "pyproject.toml" && kind != "uv.lock" && kind != "Pipfile" {
			kind = "requirements"
		}
		if raw, ok := contents[f]; ok && pyDepRe(kind, dep).Match(raw) {
			return f
		}
	}
	return ""
}

var (
	pyProjectNameRe = regexp.MustCompile(`(?ms)^\[project\]\s*$.*?^name\s*=\s*["']([^"']+)["']`)
	pyPoetryNameRe  = regexp.MustCompile(`(?ms)^\[tool\.poetry\]\s*$.*?^name\s*=\s*["']([^"']+)["']`)
	uvWorkspaceRe   = regexp.MustCompile(`(?m)^\[tool\.uv\.workspace\]`)
)

// pyServers are Python web frameworks the box runs as a generic Python
// server (framework "python"): Railpack's start command or the app's own.
var pyServers = []struct{ dep, name string }{
	{"django", "Django"},
	{"flask", "Flask"},
	{"litestar", "Litestar"},
	{"starlette", "Starlette"},
	{"quart", "Quart"},
	{"sanic", "Sanic"},
	{"python-fasthtml", "FastHTML"},
}

// guessPython reads a Python app's dependency files (dir's, by base name)
// and guesses its framework. read fetches a file's content by repository
// path.
func guessPython(dir string, files []string, read func(string) ([]byte, error)) RepoRoot {
	order := pyReadOrder(files)
	contents := map[string][]byte{}
	for _, f := range order {
		if raw, err := read(path.Join(dir, f)); err == nil {
			contents[f] = raw
		}
	}
	out := RepoRoot{Path: dir}
	if raw := contents["pyproject.toml"]; raw != nil {
		for _, re := range []*regexp.Regexp{pyProjectNameRe, pyPoetryNameRe} {
			if m := re.FindSubmatch(raw); m != nil {
				out.Name = string(m[1])
				break
			}
		}
		out.Workspace = uvWorkspaceRe.Match(raw)
	}
	for _, dep := range []string{"fastapi", "fastapi-slim"} {
		if f := pyDep(contents, order, dep); f != "" {
			out.Framework, out.Preset, out.Why = string(manifest.FrameworkFastAPI), "fastapi", "FastAPI ("+dep+" in "+f+")"
			return out
		}
	}
	out.Framework = string(manifest.FrameworkPython)
	for _, s := range pyServers {
		if f := pyDep(contents, order, s.dep); f != "" {
			out.Why = s.name + " (" + s.dep + " in " + f + "): a Python server on $PORT, started by its start command"
			return out
		}
	}
	src := "a Python project"
	if len(order) > 0 {
		src = order[0]
	}
	out.Why = "a Python server (" + src + " without FastAPI): started by its start command on $PORT"
	return out
}
