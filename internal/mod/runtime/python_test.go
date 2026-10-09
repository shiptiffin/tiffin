package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/runtime/ghapp"
)

func TestGuessPython(t *testing.T) {
	for _, c := range []struct {
		files     map[string]string
		framework string
		why       string
	}{
		{map[string]string{"pyproject.toml": "[project]\nname = \"shop-api\"\ndependencies = [\n  \"fastapi[standard]>=0.142\",\n]\n"}, "fastapi", "FastAPI (fastapi in pyproject.toml)"},
		{map[string]string{"requirements.txt": "# api\nFastAPI==0.142.2\nuvicorn[standard]\n"}, "fastapi", "FastAPI (fastapi in requirements.txt)"},
		{map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "uv.lock": "[[package]]\nname = \"fastapi\"\nversion = \"0.142.2\"\n"}, "fastapi", "FastAPI (fastapi in uv.lock)"},
		{map[string]string{"pyproject.toml": "[tool.poetry.dependencies]\npython = \"^3.13\"\nfastapi = \"^0.142\"\n"}, "fastapi", "FastAPI (fastapi in pyproject.toml)"},
		{map[string]string{"requirements.txt": "fastapi-slim==0.142\n"}, "fastapi", "FastAPI (fastapi-slim in requirements.txt)"},
		{map[string]string{"Pipfile": "[packages]\nfastapi = \"*\"\n"}, "fastapi", "FastAPI (fastapi in Pipfile)"},
		{map[string]string{"requirements.txt": "flask==3.1\ngunicorn\n"}, "python", "Flask"},
		{map[string]string{"requirements.txt": "Django>=5.2\n", "requirements-dev.txt": "pytest\n"}, "python", "Django"},
		// Names that only start like fastapi are other packages.
		{map[string]string{"requirements.txt": "fastapi-users==14\nfastapitools\n"}, "python", "a Python server (pyproject.toml"},
		{map[string]string{"pyproject.toml": "[project]\nname = \"fastapi-demo\"\ndependencies = []\n"}, "python", "a Python server (pyproject.toml without FastAPI)"},
	} {
		var names []string
		for f := range c.files {
			names = append(names, f)
		}
		g := guessPython("svc", names, func(p string) ([]byte, error) {
			if raw, ok := c.files[strings.TrimPrefix(p, "svc/")]; ok {
				return []byte(raw), nil
			}
			return nil, errors.New("no such file")
		})
		if g.Framework != c.framework || g.Path != "svc" {
			t.Errorf("%v: %s (%s), want %s", c.files, g.Framework, g.Why, c.framework)
		}
		if !strings.HasPrefix(g.Why, c.why) && !strings.HasPrefix(g.Why, strings.Replace(c.why, "pyproject.toml", "requirements.txt", 1)) {
			t.Errorf("%v: why %q, want %q…", c.files, g.Why, c.why)
		}
		if (g.Framework == "fastapi") != (g.Preset == "fastapi") {
			t.Errorf("%v: preset %q", c.files, g.Preset)
		}
	}
	g := guessPython("", []string{"pyproject.toml"}, func(string) ([]byte, error) {
		return []byte("[project]\nname = \"notes-api\"\n\n[tool.uv.workspace]\nmembers = [\"services/*\"]\n"), nil
	})
	if g.Name != "notes-api" || !g.Workspace {
		t.Errorf("a uv workspace's top: %+v", g)
	}
}

// A monorepo with JavaScript and Python apps: each app is found with its
// framework; a Python app's templates are not a static site; virtualenvs
// and caches are skipped.
func TestDetectRootsPython(t *testing.T) {
	files := map[string]string{
		"package.json":                          `{"name":"mono","workspaces":["apps/*"]}`,
		"apps/web/package.json":                 `{"name":"web","dependencies":{"next":"16"}}`,
		"apps/api/pyproject.toml":               "[project]\nname = \"api\"\ndependencies = [\"fastapi[standard]==0.142.2\"]\n",
		"apps/api/uv.lock":                      "version = 1\n",
		"apps/api/app/main.py":                  "app = FastAPI()\n",
		"apps/api/templates/index.html":         "<html>",
		"services/admin/requirements.txt":       "django==5.2\n",
		"services/admin/static/index.html":      "<html>",
		"tools/venv/lib/requirements.txt":       "fastapi\n",
		"apps/api/__pycache__/requirements.txt": "x\n",
		"apps/py-ui/package.json":               `{"name":"py-ui","scripts":{"tailwind":"tailwindcss -o out.css"}}`,
		"apps/py-ui/requirements.txt":           "fastapi\n",
	}
	var tree []ghapp.TreeEntry
	for p := range files {
		tree = append(tree, ghapp.TreeEntry{Path: p, Type: "blob"})
	}
	roots := detectRoots(tree, func(p string) ([]byte, error) {
		if raw, ok := files[p]; ok {
			return []byte(raw), nil
		}
		return nil, errors.New("no such file")
	})
	got := map[string]RepoRoot{}
	for _, r := range roots {
		got[r.Path] = r
	}
	want := map[string]string{"": "bun", "apps/web": "next", "apps/api": "fastapi", "services/admin": "python", "apps/py-ui": "fastapi"}
	for p, f := range want {
		if got[p].Framework != f {
			t.Errorf("%q: %q (%s), want %q", p, got[p].Framework, got[p].Why, f)
		}
	}
	if len(got) != len(want) {
		t.Errorf("roots: %+v", roots)
	}
	if r := got["apps/api"]; r.Name != "api" || r.Preset != "fastapi" || r.Why != "FastAPI (fastapi in pyproject.toml)" {
		t.Errorf("apps/api: %+v", r)
	}
	if r := got["services/admin"]; !strings.HasPrefix(r.Why, "Django (django in requirements.txt)") || r.Preset != "" {
		t.Errorf("services/admin: %+v", r)
	}
}

func TestFastAPIEntry(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		entry string
	}{
		{map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n\n[tool.fastapi]\nentrypoint = \"shop.web:application\"\n\n[tool.uv]\n"}, "shop.web:application"},
		{map[string]string{"app/main.py": "from fastapi import FastAPI\n\napp = FastAPI(title=\"x\")\n"}, "app.main:app"},
		{map[string]string{"main.py": "import fastapi\napi: fastapi.FastAPI = fastapi.FastAPI()\n"}, "main:api"},
		{map[string]string{"main.py": "import os\n", "api.py": "router = APIRouter()\nserver = FastAPI()\napp = FastAPI()\n"}, "api:app"},
		// A [tool.fastapi] table without an entrypoint falls back to the files.
		{map[string]string{"pyproject.toml": "[tool.fastapi]\n", "app.py": "app = FastAPI()\n"}, "app:app"},
		{map[string]string{"main.py": "def create_app():\n    return FastAPI()\n"}, ""},
	} {
		dir := t.TempDir()
		for f, body := range c.files {
			p := filepath.Join(dir, filepath.FromSlash(f))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		py, _ := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
		if got, from := fastapiEntry(dir, py); got != c.entry {
			t.Errorf("%v: %q (%s), want %q", c.files, got, from, c.entry)
		}
	}
	// The starter names its app in pyproject.toml.
	dir := filepath.Join("..", "..", "starters", "files", "fastapi")
	py, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := fastapiEntry(dir, py); got != "app.main:app" {
		t.Errorf("the starter's entrypoint: %q", got)
	}
}

func TestPythonVersion(t *testing.T) {
	for _, c := range []struct {
		requires, pin, want string
	}{
		{">=3.14", "", "3.14"},
		{">=3.14.1,<4", "", "3.14"},
		{"~=3.15.0", "", "3.15"},
		{">=3.11", "", ""},           // Railpack's default (3.13) fits
		{">=3.11,<3.13", "", "3.12"}, // upper bounds count
		{"==3.12.*", "", "3.12"},
		{"==3.12", "", "3.12"},
		{">=3.10, !=3.13.*", "", "3.14"}, // and exclusions
		{"~=3.11.2", "", "3.11"},
		{"<=3.12", "", "3.11"}, // 3.12.x is past 3.12
		{">=3.9,<3.14", "", ""},
		{">=3.13", "", ""},
		{">=3.14", ".python-version", ""}, // the app pins its own
		{"", "", ""},
	} {
		dir := t.TempDir()
		py := ""
		if c.requires != "" {
			py = "[project]\nrequires-python = \"" + c.requires + "\"\n"
		}
		if c.pin != "" {
			_ = os.WriteFile(filepath.Join(dir, c.pin), []byte("3.14\n"), 0o644)
		}
		if got, _ := pythonVersion(dir, []byte(py)); got != c.want {
			t.Errorf("requires-python %q, pin %q: %q, want %q", c.requires, c.pin, got, c.want)
		}
	}
}

// The start command a FastAPI app gets: one Uvicorn process on $PORT with
// the edge's proxy headers trusted, a keep-alive longer than the edge's and
// a graceful shutdown inside the box's 30 s; exec'd, so SIGTERM reaches it.
func TestPreparePython(t *testing.T) {
	src := t.TempDir()
	app := filepath.Join(src, "services", "api")
	_ = os.MkdirAll(filepath.Join(app, "app"), 0o755)
	_ = os.WriteFile(filepath.Join(app, "pyproject.toml"), []byte("[project]\nrequires-python = \">=3.14\"\ndependencies = [\"fastapi\", \"uvicorn[standard]\"]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(app, "app", "main.py"), []byte("app = FastAPI()\n"), 0o644)
	var log strings.Builder
	req := BuildRequest{SrcDir: src, Dir: "services/api", Spec: manifest.App{Framework: manifest.FrameworkFastAPI}, Log: &log}
	env := map[string]string{}
	if err := preparePython(req, env); err != nil {
		t.Fatal(err)
	}
	want := "cd 'services/api' && uvicorn app.main:app --host 0.0.0.0 --port $PORT --proxy-headers --forwarded-allow-ips 127.0.0.1 --timeout-keep-alive 75 --timeout-graceful-shutdown 25"
	if env["RAILPACK_START_CMD"] != want {
		t.Errorf("start: %q", env["RAILPACK_START_CMD"])
	}
	if got := execLast(env["RAILPACK_START_CMD"]); !strings.HasPrefix(got, "cd 'services/api' && exec uvicorn ") {
		t.Errorf("not exec'd: %q", got)
	}
	if env["RAILPACK_PYTHON_VERSION"] != "3.14" || !strings.Contains(log.String(), "==> FastAPI: app.main:app (app = FastAPI() in app/main.py)") ||
		strings.Contains(log.String(), "warning") {
		t.Errorf("env %v, log:\n%s", env, log.String())
	}

	// The app's own command wins; the box only picks the Python.
	env = map[string]string{}
	req.Spec.Command = "gunicorn -k uvicorn_worker.UvicornWorker app.main:app"
	if err := preparePython(req, env); err != nil || env["RAILPACK_START_CMD"] != "" {
		t.Errorf("a command of its own: %v %v", env, err)
	}
	// A generic Python app keeps Railpack's start command.
	env = map[string]string{}
	req.Spec = manifest.App{Framework: manifest.FrameworkPython}
	if err := preparePython(req, env); err != nil || env["RAILPACK_START_CMD"] != "" {
		t.Errorf("framework python: %v %v", env, err)
	}

	// No app to find, and no uvicorn: a clear error, a warning.
	_ = os.Remove(filepath.Join(app, "app", "main.py"))
	req.Spec = manifest.App{Framework: manifest.FrameworkFastAPI}
	var be *BuildError
	if err := preparePython(req, map[string]string{}); !errors.As(err, &be) || !strings.Contains(be.Hint, "[tool.fastapi] entrypoint") {
		t.Errorf("no app: %v", err)
	}
	_ = os.WriteFile(filepath.Join(app, "main.py"), []byte("app = FastAPI()\n"), 0o644)
	_ = os.WriteFile(filepath.Join(app, "pyproject.toml"), []byte("[project]\ndependencies = [\"fastapi\"]\n"), 0o644)
	log.Reset()
	if err := preparePython(req, map[string]string{}); err != nil || !strings.Contains(log.String(), "warning: uvicorn is not in the app's dependencies") {
		t.Errorf("no uvicorn: %v\n%s", err, log.String())
	}
	if planHint(req.Spec) == planHint(manifest.App{}) || onNodeHint(req.Spec) != "" {
		t.Error("a Python app's hints must not talk about package.json or Node.js")
	}
}
