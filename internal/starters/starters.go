// Package starters holds the starter apps embedded in the binary. The
// dashboard lists them (GET /v1/templates) and deploys one to an app with
// POST /v1/projects/{project}/apps/{app}/deploys/template: the same deploy
// pipeline as tiffin deploy, from source shipped inside tiffin itself.
//
// Each starter is a real, small app in files/<id>/ with its own
// tiffin.config.ts (so it also works on its own: copy the folder and run
// tiffin deploy). Its Fragment is the part of a manifest it needs, which the
// dashboard merges into the project's manifest before plan → apply.
package starters

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/btahir/tiffin/internal/manifest"
)

//go:embed all:files
var files embed.FS

// Starter describes one starter app.
type Starter struct {
	ID          string `json:"id" example:"astro" doc:"Pass as template to deploys template"`
	Name        string `json:"name" example:"Astro"`
	Description string `json:"description" doc:"One line on what it is"`
	// Kind is what you make, the choice people start from; Preset is the
	// framework within it (several per kind, one of them the default).
	Kind       string `json:"kind" enum:"web,static,api" doc:"What it makes: web (a web app with a server), static (a static site: built files, no server), api (a JSON API)"`
	Preset     string `json:"preset" example:"astro" doc:"The framework as people know it (nextjs, tanstack-start, sveltekit, react-router, nuxt, astro, vite-react, hono, fastapi, html); the same ids as a repository's detected preset"`
	PresetName string `json:"presetName" example:"Astro" doc:"The framework's display name"`
	Default    bool   `json:"default" doc:"The framework picked for its kind unless someone chooses another"`
	Listed     bool   `json:"listed" doc:"Offered when starting a project; false for demos"`
	Framework  string `json:"framework" enum:"bun,hono,next,static,fastapi,python" doc:"How the box builds and runs it (the manifest's framework); the target app must use the same one"`
	// App is the app name the fragment uses. Any name works: rename the key
	// in apps when merging the fragment.
	App      string   `json:"app" example:"site" doc:"The app name the fragment uses (rename it freely when merging)"`
	Services []string `json:"services" doc:"Services the app needs on, e.g. postgres, valkey, analytics"`
	// Fragment is the manifest part to merge into the project's manifest.
	Fragment ManifestFragment `json:"fragment" doc:"Merge into the project's manifest (apps, services, env), plan and apply, then deploy the template to the app"`
	Files    int              `json:"files" doc:"Source files shipped"`
	Bytes    int64            `json:"bytes" doc:"Source size"`
	// Edit is the file to change first.
	Edit string `json:"edit" example:"src/pages/index.astro" doc:"The file to open first"`
}

// ManifestFragment is a partial manifest: only apps and services (and env, if any).
type ManifestFragment struct {
	Apps     map[string]map[string]any `json:"apps"`
	Services map[string]map[string]any `json:"services,omitempty"`
	Env      map[string]string         `json:"env,omitempty"`
}

// meta is the catalog, in display order: per kind, its default framework
// first. A framework is listed only once its starter builds and deploys
// cleanly on a box; adding one is a folder in files/ plus a line here.
// Each starter's folder is named after its id.
type starterMeta struct {
	id, kind, preset, presetName, desc, edit string
	isDefault, listed                        bool
}

var meta = []starterMeta{
	{id: "nextjs", kind: "web", preset: "nextjs", presetName: "Next.js", isDefault: true, listed: true, edit: "app/page.jsx",
		desc: "Next.js App Router on Bun: a server component reads notes from Postgres and a server action adds them."},
	{id: "tanstack-start", kind: "web", preset: "tanstack-start", presetName: "TanStack Start", listed: true, edit: "src/routes/index.tsx",
		desc: "TanStack Start on Bun: a loader reads notes from Postgres, a server function adds them, stats stream in, and /about is prerendered."},
	{id: "sveltekit", kind: "web", preset: "sveltekit", presetName: "SvelteKit", listed: true, edit: "src/routes/+page.svelte",
		desc: "SvelteKit 3 on Bun (adapter-bun): a server load reads notes from Postgres, a form action adds them, stats stream in, and /about is prerendered."},
	{id: "react-router", kind: "web", preset: "react-router", presetName: "React Router", listed: true, edit: "app/routes/home.tsx",
		desc: "React Router 8 framework mode on Bun: a loader reads notes from Postgres, a route action adds them, stats stream in, and /about is prerendered."},
	{id: "nuxt", kind: "web", preset: "nuxt", presetName: "Nuxt", listed: true, edit: "app/pages/index.vue",
		desc: "Nuxt 4 (Nitro's node-server output on Bun): a page reads notes from Postgres through a server route, a form posts new ones, and /about is prerendered."},
	{id: "astro", kind: "static", preset: "astro", presetName: "Astro", isDefault: true, listed: true, edit: "src/pages/index.astro",
		desc: "Astro built to plain HTML: no JavaScript unless a page asks, images resized at build time, a self-hosted font."},
	{id: "vite-react", kind: "static", preset: "vite-react", presetName: "Vite + React", listed: true, edit: "src/App.tsx",
		desc: "A React single-page app built by Vite into hashed, code-split files; the box serves them."},
	{id: "hono", kind: "api", preset: "hono", presetName: "Hono", isDefault: true, listed: true, edit: "index.ts",
		desc: "A JSON API in Hono on Bun, with a Postgres table it creates on boot (list, create, update and delete notes)."},
	{id: "fastapi", kind: "api", preset: "fastapi", presetName: "FastAPI", listed: true, edit: "app/main.py",
		desc: "A JSON API in Python: FastAPI with typed Pydantic models, async SQLAlchemy on Postgres, Alembic migrations run before each release, and docs at /docs."},
	{id: "static-site", kind: "static", preset: "html", presetName: "HTML", edit: "public/index.html",
		desc: "Plain HTML and CSS served by the box's edge over HTTPS; no build and no container."},
	{id: "guestbook", kind: "web", preset: "hono", presetName: "Hono", edit: "public/index.html",
		desc: "A full-stack Hono app: a page, a JSON API, Postgres for entries, Valkey for a visit counter and cookieless analytics."},
}

// List returns every starter, in display order. Evaluating the embedded
// configs happens once per process.
func List() ([]Starter, error) {
	all, err := loadAll()
	return slices.Clone(all), err
}

var loadAll = sync.OnceValues(func() ([]Starter, error) {
	out := make([]Starter, 0, len(meta))
	for _, m := range meta {
		s, err := load(m)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
})

// Get returns one starter by its id, or false.
func Get(id string) (Starter, bool) {
	all, err := loadAll()
	if err != nil {
		return Starter{}, false
	}
	for _, s := range all {
		if s.ID == id {
			return s, true
		}
	}
	return Starter{}, false
}

// IDs lists the starter IDs.
func IDs() []string {
	ids := make([]string, len(meta))
	for i, m := range meta {
		ids[i] = m.id
	}
	return ids
}

func load(sm starterMeta) (Starter, error) {
	id := sm.id
	root := path.Join("files", id)
	raw, err := fs.ReadFile(files, path.Join(root, "tiffin.config.ts"))
	if err != nil {
		return Starter{}, err
	}
	m, err := evalConfig(raw)
	if err != nil {
		return Starter{}, fmt.Errorf("starter %s: %w", id, err)
	}
	if len(m.Apps) != 1 {
		return Starter{}, fmt.Errorf("starter %s: tiffin.config.ts must declare exactly one app", id)
	}
	s := Starter{ID: id, Name: sm.presetName, Description: sm.desc, Kind: sm.kind, Preset: sm.preset, PresetName: sm.presetName,
		Default: sm.isDefault, Listed: sm.listed, Edit: sm.edit, Services: []string{}}
	// The fragment is the starter's own config, minus the project name and
	// with defaults left out (what a person would write).
	frag, err := fragment(m)
	if err != nil {
		return Starter{}, err
	}
	s.Fragment = frag
	for app, spec := range m.Apps {
		s.App, s.Framework = app, string(spec.Framework)
	}
	for svc := range frag.Services {
		s.Services = append(s.Services, svc)
	}
	sort.Strings(s.Services)
	err = fs.WalkDir(files, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		s.Files++
		s.Bytes += info.Size()
		return nil
	})
	return s, err
}

// evalConfig evaluates an embedded tiffin.config.ts. The evaluator reads
// files, so the source goes through a temp file.
func evalConfig(src []byte) (*manifest.Manifest, error) {
	dir, err := os.MkdirTemp("", "tiffin-starter-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "tiffin.config.ts")
	if err := os.WriteFile(p, src, 0o644); err != nil {
		return nil, err
	}
	m, _, err := manifest.Load(p, nil)
	return m, err
}

// fragment renders m's apps, services and env as minimal JSON objects, using
// the same default-dropping rules as tiffin pull.
func fragment(m *manifest.Manifest) (ManifestFragment, error) {
	// RenderConfig knows which fields are defaults; evaluate its output as
	// plain JS objects by reading the evaluated, un-normalized JSON back.
	src := manifest.RenderConfig(m, "")
	dir, err := os.MkdirTemp("", "tiffin-starter-")
	if err != nil {
		return ManifestFragment{}, err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "tiffin.config.ts")
	if err := os.WriteFile(p, src, 0o644); err != nil {
		return ManifestFragment{}, err
	}
	raw, err := manifest.EvaluateJSON(p, nil)
	if err != nil {
		return ManifestFragment{}, err
	}
	var f ManifestFragment
	if err := json.Unmarshal(raw, &f); err != nil {
		return ManifestFragment{}, err
	}
	if f.Services == nil {
		f.Services = map[string]map[string]any{}
	}
	return f, nil
}

// WriteSource copies a starter's source into dir for someone to edit (tiffin
// pull): never its own tiffin.config.ts (the project's config is the truth),
// and never over a file that is already there. It returns the files written
// and the ones kept, as slash paths relative to dir.
func WriteSource(id, dir string) (written, kept []string, err error) {
	root := path.Join("files", id)
	if _, err := fs.Stat(files, root); err != nil {
		return nil, nil, fmt.Errorf("no starter %q", id)
	}
	err = fs.WalkDir(files, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, root+"/")
		if rel == "tiffin.config.ts" {
			return nil
		}
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Lstat(dest); err == nil {
			kept = append(kept, rel)
			return nil
		}
		b, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, b, 0o644); err != nil {
			return err
		}
		written = append(written, rel)
		return nil
	})
	return written, kept, err
}

// WriteTo copies a starter's files into dir.
func WriteTo(id, dir string) error {
	root := path.Join("files", id)
	if _, err := fs.Stat(files, root); err != nil {
		return fmt.Errorf("no starter %q", id)
	}
	return fs.WalkDir(files, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		b, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0o644)
	})
}
