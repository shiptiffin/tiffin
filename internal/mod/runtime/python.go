package runtime

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/manifest"
)

// Python apps build with Railpack's Python provider (uv, pip, Poetry, PDM
// or Pipenv, by the lockfile). The box adds two things: a Python version
// that satisfies requires-python when the app pins none, and, for FastAPI,
// the start command.

// railpackPythonDefault is the Python Railpack installs when nothing pins
// one (its DEFAULT_PYTHON_VERSION).
const railpackPythonDefault = "3.13"

// pythonVersionFiles pin an app's Python for Railpack (mise reads them).
var pythonVersionFiles = []string{".python-version", ".tool-versions", "mise.toml", ".mise.toml", "runtime.txt", "Pipfile"}

// uvicornFlags start FastAPI as one Uvicorn process, as the box runs apps:
//   - --proxy-headers from 127.0.0.1 only: the box's edge connects from
//     there and sets X-Forwarded-For and -Proto, so request.client and
//     url_for see the visitor and https.
//   - --timeout-keep-alive 75: longer than the edge keeps an idle
//     connection to the app (60 s), so the app never closes one the edge is
//     about to reuse (a 502 for a POST, which is not retried).
//   - --timeout-graceful-shutdown 25: the box sends SIGTERM once requests in
//     flight are done and kills 30 s later; work after a response
//     (background tasks) gets 25 s, then the app's lifespan shutdown runs.
const uvicornFlags = "--host 0.0.0.0 --port $PORT --proxy-headers --forwarded-allow-ips 127.0.0.1 --timeout-keep-alive 75 --timeout-graceful-shutdown 25"

// fastapiStart is the start command for a FastAPI app at entry ("app.main:app").
func fastapiStart(entry string) string { return "uvicorn " + entry + " " + uvicornFlags }

// preparePython sets a Python app's build env: its Python version, and the
// start command of a FastAPI app that sets no command.
func preparePython(req BuildRequest, env map[string]string) error {
	dir := req.appDir()
	pyproject, _ := readSrc(filepath.Join(dir, "pyproject.toml"))
	if v, why := pythonVersion(dir, pyproject); v != "" {
		env["RAILPACK_PYTHON_VERSION"] = v
		fmt.Fprintf(req.Log, "==> Python %s (%s)\n", v, why)
	}
	if req.Spec.Framework != manifest.FrameworkFastAPI || req.Spec.Command != "" {
		return nil
	}
	entry, from := fastapiEntry(dir, pyproject)
	if entry == "" {
		return &BuildError{Msg: "the box could not find the FastAPI app (app = FastAPI() in main.py, app.py, api.py or app/main.py)",
			Hint: "Name it in pyproject.toml ([tool.fastapi] entrypoint = \"pkg.main:app\"), or set the app's command in tiffin.config.ts (e.g. \"uvicorn pkg.main:app --host 0.0.0.0 --port $PORT\")."}
	}
	env["RAILPACK_START_CMD"] = req.inApp(fastapiStart(entry))
	fmt.Fprintf(req.Log, "==> FastAPI: %s (%s), one Uvicorn process on $PORT\n", entry, from)
	if !pyUses(dir, "uvicorn", "fastapi[standard", "fastapi[all", "fastapi-cli[standard") {
		fmt.Fprintf(req.Log, "==> warning: uvicorn is not in the app's dependencies, and the start command needs it: add uvicorn[standard] (uv add 'uvicorn[standard]')\n")
	}
	return nil
}

var requiresPythonRe = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*["']([^"']+)["']`)

// pythonMinors are the Pythons the box may ask Railpack for, newest last.
var pythonMinors = []string{"3.9", "3.10", "3.11", "3.12", "3.13", "3.14"}

// pythonVersion is the Python to ask Railpack for ("" leaves its choice):
// when the app pins no version (a .python-version, mise or runtime.txt
// file) and Railpack's default does not satisfy pyproject.toml's
// requires-python, the newest Python that satisfies all of it (upper
// bounds and exclusions too), else the lowest its lower bound names.
func pythonVersion(dir string, pyproject []byte) (string, string) {
	for _, f := range pythonVersionFiles {
		if exists(filepath.Join(dir, f)) {
			return "", ""
		}
	}
	m := requiresPythonRe.FindSubmatch(pyproject)
	if m == nil {
		return "", ""
	}
	req := string(m[1])
	why := "requires-python " + req + " in pyproject.toml"
	if pySatisfies(railpackPythonDefault, req) {
		return "", ""
	}
	for _, v := range slices.Backward(pythonMinors) {
		if pySatisfies(v, req) {
			return v, why
		}
	}
	low := ""
	for _, spec := range strings.Split(req, ",") {
		spec = strings.TrimSpace(spec)
		for _, op := range []string{">=", "~=", "=="} {
			if v, ok := strings.CutPrefix(spec, op); ok {
				low = minorOf(strings.TrimSuffix(strings.TrimSpace(v), ".*"))
			}
		}
	}
	if low == "" || !versionLess(railpackPythonDefault, low) {
		return "", ""
	}
	return low, why
}

var pySpecRe = regexp.MustCompile(`^(===|==|!=|~=|<=|>=|<|>)\s*v?([0-9]+(?:\.[0-9]+)*)(\.\*)?$`)

// pySatisfies reports whether Python minor (the latest patch release of
// it, which is what gets installed) meets a PEP 440 specifier set such as
// ">=3.11,<3.13,!=3.12.*". A specifier it cannot read counts as met.
func pySatisfies(minor, specs string) bool {
	v := minor + ".99"
	for _, spec := range strings.Split(specs, ",") {
		m := pySpecRe.FindStringSubmatch(strings.TrimSpace(spec))
		if m == nil {
			continue
		}
		op, want, wild := m[1], m[2], m[3] != ""
		prefix := func() bool { // v is want, or a release of it (3.12.* holds 3.12.4)
			return v == want || strings.HasPrefix(v, want+".")
		}
		ok := true
		switch op {
		case "==", "===":
			// ==3.12 asks for that minor in practice (an exact 3.12.0 is
			// not something one installs), so it matches as 3.12.* does.
			ok = ((wild || strings.Count(want, ".") < 2) && prefix()) || pyCmp(v, want) == 0
		case "!=":
			ok = !((wild && prefix()) || (!wild && pyCmp(v, want) == 0))
		case ">=":
			ok = pyCmp(v, want) >= 0
		case "<=":
			ok = pyCmp(v, want) <= 0
		case ">":
			ok = pyCmp(v, want) > 0
		case "<":
			ok = pyCmp(v, want) < 0
		case "~=": // ~=3.11.2: >=3.11.2 and ==3.11.*; ~=3.11: >=3.11 and ==3.*
			parts := strings.Split(want, ".")
			if len(parts) < 2 {
				continue
			}
			pre := strings.Join(parts[:len(parts)-1], ".")
			ok = pyCmp(v, want) >= 0 && (v == pre || strings.HasPrefix(v, pre+"."))
		}
		if !ok {
			return false
		}
	}
	return true
}

// pyCmp compares release versions, missing parts as 0 (3.12 == 3.12.0).
func pyCmp(a, b string) int {
	switch {
	case versionLess(a, b):
		return -1
	case versionLess(b, a):
		return 1
	}
	return 0
}

// minorOf cuts "3.14.2" to "3.14".
func minorOf(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// versionLess compares dotted numbers ("3.9" < "3.13").
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

var (
	fastapiEntrypointRe = regexp.MustCompile(`(?ms)^\[tool\.fastapi\]\s*$(.*?)(?:^\[|\z)`)
	entrypointKeyRe     = regexp.MustCompile(`(?m)^\s*entrypoint\s*=\s*["']([\w.]+:[\w.]+)["']`)
	// fastapiAppRe finds `app = FastAPI(` (or `api: FastAPI = fastapi.FastAPI(`) at the top level.
	fastapiAppRe = regexp.MustCompile(`(?m)^([A-Za-z_]\w*)\s*(?::[^=\n]+)?=\s*(?:fastapi\.)?FastAPI\(`)
)

// fastapiFiles are where FastAPI's CLI looks for the app, in its order.
var fastapiFiles = []string{"main.py", "app.py", "api.py", "app/main.py", "app/app.py", "app/api.py"}

// fastapiEntry finds a FastAPI app's import string ("app.main:app") and
// where it came from: pyproject.toml's [tool.fastapi] entrypoint (what
// `fastapi run` reads), else an `app = FastAPI()` in the files FastAPI's CLI
// looks in.
func fastapiEntry(dir string, pyproject []byte) (entry, from string) {
	if sec := fastapiEntrypointRe.FindSubmatch(pyproject); sec != nil {
		if m := entrypointKeyRe.FindSubmatch(sec[1]); m != nil {
			return string(m[1]), "[tool.fastapi] entrypoint in pyproject.toml"
		}
	}
	for _, f := range fastapiFiles {
		raw, err := readSrc(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		var names []string
		for _, m := range fastapiAppRe.FindAllSubmatch(raw, -1) {
			names = append(names, string(m[1]))
		}
		if len(names) == 0 {
			continue
		}
		name := names[0] // FastAPI's CLI prefers app, then api
		if slices.Contains(names, "app") {
			name = "app"
		} else if slices.Contains(names, "api") {
			name = "api"
		}
		module := strings.ReplaceAll(strings.TrimSuffix(f, ".py"), "/", ".")
		return module + ":" + name, name + " = FastAPI() in " + f
	}
	return "", ""
}

// pyUses reports whether any of the app's dependency files names one of
// deps (a prefix such as "fastapi[standard" matches its extras too).
func pyUses(dir string, deps ...string) bool {
	for _, f := range []string{"pyproject.toml", "requirements.txt", "uv.lock", "Pipfile", "poetry.lock", "pdm.lock"} {
		raw, err := readSrc(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		low := strings.ToLower(string(raw))
		for _, d := range deps {
			if strings.Contains(d, "[") {
				if strings.Contains(low, d) {
					return true
				}
				continue
			}
			kind := f
			if f == "poetry.lock" || f == "pdm.lock" {
				kind = "uv.lock" // [[package]] name = "..."
			} else if f == "requirements.txt" {
				kind = "requirements"
			}
			if pyDepRe(kind, d).MatchString(low) {
				return true
			}
		}
	}
	return false
}

// planHint says what Railpack needs to plan the app's build.
func planHint(spec manifest.App) string {
	if spec.Framework.IsPython() {
		return "Make sure the app has a pyproject.toml with its lockfile (uv.lock, poetry.lock or pdm.lock), or a requirements.txt. See the build log for details."
	}
	return "Make sure the app has a package.json with a start script (or an index.ts), and a lockfile. See the build log for details."
}
