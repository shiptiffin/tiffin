package portable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/change/changetest"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime"
	"github.com/btahir/tiffin/internal/mod/storage"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
)

// fakeBackend keeps a box's services in memory: a database is its SQL
// text, the cache a map, apps their live deploys; buckets and git
// repositories are real directories.
type fakeBackend struct {
	mu       sync.Mutex
	p        *platform.Platform
	root     string
	dbs      map[string]string
	ext      map[string][]string
	keys     map[string]map[string]cacheEntry
	releases map[string]*runtime.Deploy // project/app → live deploy
	images   map[string]string          // ref → content
	deleted  map[string]bool
	failApp  string // releases of this app fail, as a load that is refused
}

func newFake(root string) *fakeBackend {
	return &fakeBackend{root: root, dbs: map[string]string{}, ext: map[string][]string{}, keys: map[string]map[string]cacheEntry{},
		releases: map[string]*runtime.Deploy{}, images: map[string]string{}, deleted: map[string]bool{}}
}

func (f *fakeBackend) databaseInfo(_ context.Context, project string) (*PostgresInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.dbs[project]; !ok {
		return nil, errors.New("no database")
	}
	return &PostgresInfo{Database: "p_" + project, Role: "p_" + project, Extensions: f.ext[project]}, nil
}

func (f *fakeBackend) dumpDatabase(_ context.Context, project string, w io.Writer) error {
	f.mu.Lock()
	s := f.dbs[project]
	f.mu.Unlock()
	_, err := io.WriteString(w, s)
	return err
}

func (f *fakeBackend) prepareDatabase(_ context.Context, project string, manifestExt, extra []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.dbs[project]; ok {
		return errors.New("database exists")
	}
	f.dbs[project] = ""
	f.ext[project] = slices.Compact(slices.Sorted(slices.Values(append(append([]string{}, manifestExt...), extra...))))
	return nil
}

func (f *fakeBackend) restoreDatabase(_ context.Context, project string, r io.Reader) error {
	b, err := io.ReadAll(r)
	f.mu.Lock()
	f.dbs[project] = string(b)
	f.mu.Unlock()
	return err
}

func (f *fakeBackend) dropDatabase(_ context.Context, project string) error {
	f.mu.Lock()
	delete(f.dbs, project)
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) restoreCron(context.Context, string, []cronJob) error { return nil }

func (f *fakeBackend) dumpKeys(_ context.Context, project string, each func(cacheEntry) error) error {
	f.mu.Lock()
	var es []cacheEntry
	for _, e := range f.keys[project] {
		es = append(es, e)
	}
	f.mu.Unlock()
	sort.Slice(es, func(i, j int) bool { return es[i].Key < es[j].Key })
	for _, e := range es {
		if err := each(e); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeBackend) restoreKeys(_ context.Context, project string, keys func(func(cacheEntry) error) error) error {
	return keys(func(e cacheEntry) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.keys[project] == nil {
			f.keys[project] = map[string]cacheEntry{}
		}
		f.keys[project][e.Key] = e
		return nil
	})
}

func (f *fakeBackend) liveRelease(_ context.Context, project, app string) (*runtime.Deploy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases[project+"/"+app], nil
}

func (f *fakeBackend) saveImage(_ context.Context, ref string, w io.Writer) error {
	f.mu.Lock()
	s, ok := f.images[ref]
	f.mu.Unlock()
	if !ok {
		return errors.New("no image " + ref)
	}
	_, err := io.WriteString(w, s)
	return err
}

func (f *fakeBackend) release(_ context.Context, project, app string, src runtime.ReleaseSource, by string) (*runtime.Deploy, error) {
	d := &runtime.Deploy{ID: ids.New("dep"), Project: project, App: app, Status: runtime.StatusLive, Source: runtime.SourcePrebuilt,
		Framework: src.Framework, Commit: src.Commit, Repo: src.Repo, CreatedBy: by, URL: "https://" + project + ".example"}
	ref := "docker.io/tiffin/" + project + "-" + app + ":" + strings.ToLower(d.ID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if app == f.failApp {
		d.Status, d.URL, d.Error, d.Hint = runtime.StatusFailed, "", "the tarball contained no image", "Pass a tarball from `docker save <image>`."
		return d, nil
	}
	switch {
	case src.StaticDir != "":
		d.StaticRoot = filepath.Join(f.root, "static", project, app, d.ID)
		_ = os.MkdirAll(filepath.Dir(d.StaticRoot), 0o755)
		if err := os.Rename(src.StaticDir, d.StaticRoot); err != nil {
			return nil, err
		}
	case src.ImageTar != "":
		b, err := os.ReadFile(src.ImageTar)
		if err != nil {
			return nil, err
		}
		f.images[ref], d.Image = string(b), ref
	case src.Image != "":
		s, ok := f.images[src.Image]
		if !ok {
			d.Status, d.Error = runtime.StatusFailed, "no image "+src.Image
			return d, nil
		}
		f.images[ref], d.Image = s, ref
	}
	f.releases[project+"/"+app] = d
	return d, nil
}

func (f *fakeBackend) gitDir(project string) string {
	return filepath.Join(f.root, "git", project+".git")
}

func (f *fakeBackend) diskDir(project, app string) string {
	return filepath.Join(f.root, "disks", project, app, "prod")
}

func (f *fakeBackend) bucketDir(project, bucket string) string {
	return filepath.Join(f.root, "buckets", storage.S3Name(project, bucket))
}

// converged makes the buckets, as the storage reconcile would.
func (f *fakeBackend) converged(ctx context.Context, project string) error {
	_, res, err := f.p.DB.Load(ctx, project)
	if err != nil {
		return err
	}
	for addr := range res {
		if change.Kind(addr) == change.KindBucket {
			if err := os.MkdirAll(f.bucketDir(project, change.Name(addr)), 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeBackend) recentlyDeleted(_ context.Context, project string) bool {
	return f.deleted[project]
}

// testBox is a box with a fake backend: real state, change engine and box key.
type testBox struct {
	t  *testing.T
	p  *platform.Platform
	b  *fakeBackend
	m  *Module
	pr *tokens.Principal
}

func newTestBox(t *testing.T, domain string) *testBox {
	t.Helper()
	root := t.TempDir()
	db := openState(t, filepath.Join(root, "state.db"))
	t.Cleanup(func() { db.Close() })
	home := filepath.Join(root, "platform")
	_ = os.MkdirAll(home, 0o700)
	sec, err := platform.OpenSecrets(db, home)
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Secrets: sec, Home: home, DataRoot: root, Domain: domain,
		PublicURL: "https://dashboard." + domain, Version: "dev", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	b := newFake(filepath.Join(root, "services"))
	b.p = p
	return &testBox{t: t, p: p, b: b, m: &Module{be: b},
		pr: &tokens.Principal{TokenID: "tok_owner", Name: "owner", Kind: tokens.KindOwner, Scopes: []tokens.Scope{tokens.ScopeAll}, Projects: []string{"*"}}}
}

// shop makes project shop: Postgres, Valkey, two buckets, a container app
// with a custom domain and GitHub deploys, a static site, a worker that
// never deployed, a secret, a storage limit and a git repository.
func (x *testBox) shop(name string) {
	t, ctx := x.t, context.Background()
	m := changetest.M(name, func(m *manifest.Manifest) {
		m.Services = manifest.Services{Postgres: &manifest.Postgres{Extensions: []string{"vector"}}, Valkey: &manifest.Valkey{MaxMemoryMB: 32},
			Storage: &manifest.Storage{Buckets: map[string]manifest.Bucket{"media": {Public: true}, "docs": {}}}}
		m.Apps = map[string]manifest.App{
			"web":  {Path: ".", Framework: manifest.FrameworkBun, Role: manifest.RoleWeb, Routes: []string{name, "example.com"}, Instances: 1, Git: &manifest.Git{Repo: "acme/shop", Branch: "main", Previews: manifest.PreviewsSameRepo}, Disk: []string{"data"}},
			"site": {Path: ".", Framework: manifest.FrameworkStatic, Role: manifest.RoleWeb, Routes: []string{name + "-docs"}, Instances: 1},
			"jobs": {Path: ".", Framework: manifest.FrameworkBun, Role: manifest.RoleWorker, Instances: 1},
		}
		m.Domains = map[string]manifest.Domain{"example.com": {}}
		m.Env = map[string]string{"GREETING": "hello", "PRICE": "$5"}
	})
	changetest.Converge(t, x.p.Engine, m)
	if _, err := x.p.SetSecrets(ctx, name, map[string]string{"API_KEY": "sk-" + name}, "set a key"); err != nil {
		t.Fatal(err)
	}
	plan, _ := x.p.Engine.PlanEdit(ctx, name, func(cur map[string]change.Resource) (map[string]change.Resource, error) {
		cur[change.KindStorageLimit] = change.Resource{Address: change.KindStorageLimit, Spec: json.RawMessage(`{"maxBytes":1000000}`)}
		return cur, nil
	})
	if _, err := x.p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash}); err != nil {
		t.Fatal(err)
	}
	b := x.b
	b.dbs[name] = "CREATE TABLE notes(id int);\nINSERT INTO notes VALUES (1); -- " + name + "\n"
	b.ext[name] = []string{"pgcrypto", "vector"}
	b.keys[name] = map[string]cacheEntry{"greeting": {Key: "greeting", Dump: []byte("hello\x00\xff" + name)}, "session:1": {Key: "session:1", TTLMs: 60000, Dump: []byte("s")}}
	for bucket, files := range map[string]map[string]string{"media": {"notes/a.txt": "written in " + name, "logo.png": "PNG"}, "docs": {"readme.md": "# docs"}} {
		for rel, body := range files {
			path := filepath.Join(b.bucketDir(name, bucket), rel)
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = os.MkdirAll(filepath.Join(b.diskDir(name, "web"), "data", "db"), 0o755)
	_ = os.WriteFile(filepath.Join(b.diskDir(name, "web"), "data", "db", "app.sqlite"), []byte("rows of "+name), 0o644)
	_ = os.MkdirAll(filepath.Join(b.bucketDir(name, "media"), ".sgwtmp", "multipart"), 0o755)
	_ = os.WriteFile(filepath.Join(b.bucketDir(name, "media"), ".sgwtmp", "multipart", "part"), []byte("half an upload"), 0o644)
	ref := "docker.io/tiffin/" + name + "-web:dep_1"
	b.images[ref] = "IMAGE of " + name
	b.releases[name+"/web"] = &runtime.Deploy{ID: "dep_1", Project: name, App: "web", Status: runtime.StatusLive, Image: ref, Framework: "bun", Commit: "abc123", URL: "https://" + name + ".example"}
	site := filepath.Join(b.root, "static", name, "site", "dep_2")
	_ = os.MkdirAll(site, 0o755)
	_ = os.WriteFile(filepath.Join(site, "index.html"), []byte("<h1>"+name+" docs</h1>"), 0o644)
	b.releases[name+"/site"] = &runtime.Deploy{ID: "dep_2", Project: name, App: "site", Status: runtime.StatusLive, StaticRoot: site, Framework: "static+spa"}
	_ = os.MkdirAll(b.gitDir(name), 0o755)
	_ = os.WriteFile(filepath.Join(b.gitDir(name), "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
}

func (x *testBox) export(project string, o exportOptions) []byte {
	x.t.Helper()
	o.stage = filepath.Join(x.p.DataRoot, "stage-"+ids.New("t"))
	var buf bytes.Buffer
	if _, err := writeProject(context.Background(), x.p, x.b, project, o, reporter{}, &buf); err != nil {
		x.t.Fatal(err)
	}
	return buf.Bytes()
}

func (x *testBox) importArchive(raw []byte, o importOptions) (*imported, error) {
	x.t.Helper()
	o.stage = filepath.Join(x.p.DataRoot, "stage-"+ids.New("t"))
	if o.principal == nil {
		o.principal = x.pr
	}
	return importProject(context.Background(), x.p, x.b, bytes.NewReader(raw), o, reporter{})
}

// entries reads an archive: names in order, and file contents.
func entries(t *testing.T, raw []byte) (*boxfile.Manifest, []string, map[string]string) {
	t.Helper()
	ar, err := boxfile.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	var names []string
	files := map[string]string{}
	for {
		e, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, e.Name)
		b, _ := io.ReadAll(e.Body)
		files[e.Name] = string(b)
	}
	return &ar.Manifest, names, files
}

// TestProjectExport: one project's archive holds that project and nothing
// of the others, in the order an import reads it, as ordinary files.
func TestProjectExport(t *testing.T) {
	x := newTestBox(t, "a.example")
	x.shop("shop")
	x.shop("blog")
	man, names, files := entries(t, x.export("shop", exportOptions{}))
	if man.Kind != boxfile.ProjectKind || man.Project != "shop" || strings.Join(man.Projects, ",") != "shop" || man.Recipient == "" {
		t.Fatalf("manifest: %+v", man)
	}
	want := []string{"project.json", "README.md", "tiffin.config.ts", "docker-compose.yml", "secrets.json", "database-setup.sql", "database.sql", "cache.jsonl",
		"files/docs/", "files/docs/readme.md", "files/media/", "files/media/logo.png", "files/media/notes/", "files/media/notes/a.txt",
		"disk/web/", "disk/web/data/", "disk/web/data/db/", "disk/web/data/db/app.sqlite",
		"source.git/", "source.git/HEAD", "apps/site/release.json", "apps/site/site/", "apps/site/site/index.html", "apps/web/release.json", "apps/web/image.tar"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries:\n got %v\nwant %v", names, want)
	}
	for name, body := range files {
		if strings.Contains(body, "blog") && name != "README.md" {
			t.Errorf("%s holds another project's data: %q", name, body)
		}
	}
	if files["database.sql"] != x.b.dbs["shop"] || files["apps/web/image.tar"] != "IMAGE of shop" || files["files/media/notes/a.txt"] != "written in shop" {
		t.Fatalf("contents: %q %q", files["database.sql"], files["apps/web/image.tar"])
	}
	if strings.Contains(files["secrets.json"], "sk-shop") || !strings.Contains(files["secrets.json"], man.Recipient) {
		t.Fatalf("sealed secrets: %s", files["secrets.json"])
	}
	if !strings.Contains(files["database-setup.sql"], `CREATE EXTENSION IF NOT EXISTS "vector"`) || !strings.Contains(files["database-setup.sql"], "tiffin.org_id()") {
		t.Fatalf("setup: %s", files["database-setup.sql"])
	}
	if lines := strings.Split(strings.TrimSpace(files["cache.jsonl"]), "\n"); len(lines) != 2 || !strings.Contains(lines[0], `"key":"greeting"`) {
		t.Fatalf("cache: %s", files["cache.jsonl"])
	}
	var info ProjectInfo
	if err := json.Unmarshal([]byte(files["project.json"]), &info); err != nil {
		t.Fatal(err)
	}
	if info.Project != "shop" || !info.Git || len(info.Buckets) != 2 || len(info.Apps) != 3 || !slices.Equal(info.Secrets.Names, []string{"API_KEY"}) ||
		info.Postgres == nil || info.Valkey.Prefix != "p_shop:" || string(compactJSON(info.StorageLimit)) != `{"maxBytes":1000000}` {
		t.Fatalf("project.json: %+v", info)
	}
	if !strings.Contains(files["tiffin.config.ts"], `project: "shop"`) {
		t.Fatalf("config: %s", files["tiffin.config.ts"])
	}

	// With secrets and History: a plain .env, the changes oldest first.
	_, names, files = entries(t, x.export("shop", exportOptions{includeSecrets: true, withHistory: true}))
	if slices.Contains(names, "secrets.json") || files[".env"] != "# Secrets of this project, in plain text. Keep this file private.\nAPI_KEY=\"sk-shop\"\n" {
		t.Fatalf(".env: %v %q", names, files[".env"])
	}
	hist := strings.Split(strings.TrimSpace(files["history/changes.jsonl"]), "\n")
	if len(hist) != 3 || !strings.Contains(hist[0], `"version":1`) || !strings.Contains(hist[2], `"version":3`) {
		t.Fatalf("history: %d lines", len(hist))
	}
}

// TestProjectImportAlongside: an import makes a new project beside the
// others and never replaces one; under another name the apps get its own
// addresses, and custom domains and GitHub deploys stay behind.
func TestProjectImportAlongside(t *testing.T) {
	ctx := context.Background()
	x := newTestBox(t, "a.example")
	x.shop("shop")
	raw := x.export("shop", exportOptions{})
	var prob *api.Problem
	if _, err := x.importArchive(raw, importOptions{}); !errors.As(err, &prob) || prob.Status != 409 || !strings.Contains(prob.Hint, "--name") {
		t.Fatalf("same name: %v", err)
	}
	x.b.deleted["gone"] = true
	if _, err := x.importArchive(raw, importOptions{name: "gone"}); !errors.As(err, &prob) || !strings.Contains(err.Error(), "destroyed") {
		t.Fatalf("recently destroyed name: %v", err)
	}
	if len(x.b.dbs) != 1 {
		t.Fatalf("a refused import touched Postgres: %v", x.b.dbs)
	}

	res, err := x.importArchive(raw, importOptions{name: "shop-2", intent: "Imported for a test"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.created || res.project != "shop-2" || len(res.missing) != 0 {
		t.Fatalf("result: %+v", res)
	}
	_, got, _ := x.p.DB.Load(ctx, "shop-2")
	var web manifest.App
	_ = json.Unmarshal(got["app/web"].Spec, &web)
	if !slices.Equal(web.Routes, []string{"shop-2"}) || web.Git != nil {
		t.Fatalf("web routes %v git %v", web.Routes, web.Git)
	}
	var site manifest.App
	_ = json.Unmarshal(got["app/site"].Spec, &site)
	if !slices.Equal(site.Routes, []string{"shop-2-docs"}) {
		t.Fatalf("site routes %v", site.Routes)
	}
	if _, ok := got["domain/example.com"]; ok {
		t.Fatal("the custom domain came along")
	}
	if string(got[change.KindStorageLimit].Spec) != `{"maxBytes":1000000}` {
		t.Fatalf("storage limit: %s", got[change.KindStorageLimit].Spec)
	}
	if !strings.Contains(strings.Join(res.notes, " "), "example.com") || !strings.Contains(strings.Join(res.notes, " "), "acme/shop") {
		t.Fatalf("notes: %v", res.notes)
	}
	sec, err := x.p.Secrets.All(ctx, "shop-2")
	if err != nil || sec["API_KEY"] != "sk-shop" {
		t.Fatalf("secrets: %v %v", sec, err)
	}
	if x.b.dbs["shop-2"] != x.b.dbs["shop"] || !slices.Equal(x.b.ext["shop-2"], []string{"pgcrypto", "vector"}) {
		t.Fatalf("database: %q ext %v", x.b.dbs["shop-2"], x.b.ext["shop-2"])
	}
	if e := x.b.keys["shop-2"]["greeting"]; string(e.Dump) != "hello\x00\xffshop" || x.b.keys["shop-2"]["session:1"].TTLMs != 60000 {
		t.Fatalf("cache: %+v", x.b.keys["shop-2"])
	}
	if b, _ := os.ReadFile(filepath.Join(x.b.bucketDir("shop-2", "media"), "notes", "a.txt")); string(b) != "written in shop" {
		t.Fatalf("object: %q", b)
	}
	if _, err := os.Stat(filepath.Join(x.b.bucketDir("shop-2", "media"), ".sgwtmp")); err == nil {
		t.Fatal("the gateway's scratch space came along")
	}
	if _, err := os.Stat(filepath.Join(x.b.gitDir("shop-2"), "HEAD")); err != nil {
		t.Fatal("git repository not copied")
	}
	if b, _ := os.ReadFile(filepath.Join(x.b.diskDir("shop-2", "web"), "data", "db", "app.sqlite")); string(b) != "rows of shop" {
		t.Fatalf("disk folder: %q", b)
	}
	if len(res.apps) != 3 || res.apps[0].App != "jobs" || res.apps[0].Status != "none" {
		t.Fatalf("apps: %+v", res.apps)
	}
	webDep, siteDep := x.b.releases["shop-2/web"], x.b.releases["shop-2/site"]
	if webDep == nil || x.b.images[webDep.Image] != "IMAGE of shop" || webDep.Commit != "abc123" {
		t.Fatalf("web release: %+v", webDep)
	}
	if siteDep == nil || siteDep.Framework != "static+spa" || readFile(t, filepath.Join(siteDep.StaticRoot, "index.html")) != "<h1>shop docs</h1>" {
		t.Fatalf("site release: %+v", siteDep)
	}
	hist, _ := x.p.DB.ListChanges(ctx, change.ListFilter{Project: "shop-2"})
	if len(hist) != 1 || hist[0].Intent != "Imported for a test" || hist[0].Actor.ID != "tok_owner" {
		t.Fatalf("history: %+v", hist)
	}
	// The original is untouched.
	if _, res, _ := x.p.DB.Load(ctx, "shop"); len(res) == 0 || x.b.releases["shop/web"].ID != "dep_1" {
		t.Fatal("the original changed")
	}
}

// TestDuplicate: a copy has the same data, starts its own History and is
// independent of the original afterwards.
func TestDuplicate(t *testing.T) {
	ctx := context.Background()
	x := newTestBox(t, "a.example")
	x.shop("shop")
	res, err := x.m.duplicate(ctx, x.p, x.pr, "shop", "shop-copy", reporter{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.created || x.b.dbs["shop-copy"] != x.b.dbs["shop"] || len(x.b.keys["shop-copy"]) != 2 {
		t.Fatalf("copy: %+v db %q", res, x.b.dbs["shop-copy"])
	}
	web := x.b.releases["shop-copy/web"]
	if web == nil || web.Image == x.b.releases["shop/web"].Image || x.b.images[web.Image] != "IMAGE of shop" {
		t.Fatalf("the copy's image is its own tag of the same image: %+v", web)
	}
	hist, _ := x.p.DB.ListChanges(ctx, change.ListFilter{Project: "shop-copy"})
	if len(hist) != 1 || hist[0].Intent != "Duplicated from shop" {
		t.Fatalf("history: %+v", hist)
	}
	if sec, _ := x.p.Secrets.All(ctx, "shop-copy"); sec["API_KEY"] != "sk-shop" {
		t.Fatalf("secrets: %v", sec)
	}
	// Independent afterwards.
	x.b.dbs["shop"] += "DELETE FROM notes;\n"
	x.b.keys["shop"]["greeting"] = cacheEntry{Key: "greeting", Dump: []byte("changed")}
	_ = os.WriteFile(filepath.Join(x.b.bucketDir("shop", "media"), "notes", "a.txt"), []byte("changed"), 0o644)
	_ = os.WriteFile(filepath.Join(x.b.diskDir("shop", "web"), "data", "db", "app.sqlite"), []byte("changed"), 0o644)
	if strings.Contains(x.b.dbs["shop-copy"], "DELETE") || string(x.b.keys["shop-copy"]["greeting"].Dump) == "changed" ||
		readFile(t, filepath.Join(x.b.bucketDir("shop-copy", "media"), "notes", "a.txt")) != "written in shop" ||
		readFile(t, filepath.Join(x.b.diskDir("shop-copy", "web"), "data", "db", "app.sqlite")) != "rows of shop" {
		t.Fatal("the copy follows the original")
	}
	if _, err := x.m.duplicate(ctx, x.p, x.pr, "shop", "shop-copy", reporter{}); err == nil {
		t.Fatal("duplicating onto an existing project")
	}
	if _, err := x.m.duplicate(ctx, x.p, x.pr, "nope", "nope-copy", reporter{}); !errors.Is(err, errNotFoundProject) {
		t.Fatalf("missing project: %v", err)
	}
}

// TestImportSecretsFromAnotherBox: sealed secrets need the source box's key;
// without it they are left out and listed; a plain .env needs nothing.
func TestImportSecretsFromAnotherBox(t *testing.T) {
	ctx := context.Background()
	a, b := newTestBox(t, "a.example"), newTestBox(t, "b.example")
	a.shop("shop")
	raw := a.export("shop", exportOptions{withHistory: true})

	res, err := b.importArchive(raw, importOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.missing, []string{"API_KEY"}) || !strings.Contains(strings.Join(res.notes, " "), "tiffin secrets set shop") {
		t.Fatalf("missing secrets: %+v", res)
	}
	if sec, _ := b.p.Secrets.All(ctx, "shop"); len(sec) != 0 {
		t.Fatalf("secrets set without the key: %v", sec)
	}
	// History came along, before the import's own change.
	hist, _ := b.p.DB.ListChanges(ctx, change.ListFilter{Project: "shop"})
	if len(hist) != 4 || hist[0].Version != 4 || hist[3].Version != 1 || hist[2].Intent != "set a key" {
		t.Fatalf("history: %d changes", len(hist))
	}

	// With the source box's key, they are sealed again to this box's.
	key, err := os.ReadFile(filepath.Join(a.p.Home, "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(key)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.importArchive(raw, importOptions{name: "shop-keyed", sourceKey: id}); err != nil {
		t.Fatal(err)
	}
	if sec, err := b.p.Secrets.All(ctx, "shop-keyed"); err != nil || sec["API_KEY"] != "sk-shop" {
		t.Fatalf("re-sealed secrets: %v %v", sec, err)
	}
	// A plain .env needs no key; withoutSecrets leaves even those out.
	plain := a.export("shop", exportOptions{includeSecrets: true})
	if _, err := b.importArchive(plain, importOptions{name: "shop-plain"}); err != nil {
		t.Fatal(err)
	}
	if sec, _ := b.p.Secrets.All(ctx, "shop-plain"); sec["API_KEY"] != "sk-shop" {
		t.Fatalf(".env secrets: %v", sec)
	}
	res, err = b.importArchive(plain, importOptions{name: "shop-bare", skipSecrets: true})
	if err != nil || !slices.Equal(res.missing, []string{"API_KEY"}) {
		t.Fatalf("without secrets: %v %+v", err, res)
	}
}

// TestImportFailureLeavesNothing: a failure before the project exists
// drops the database it prepared and records no History.
func TestImportFailureLeavesNothing(t *testing.T) {
	ctx := context.Background()
	a, b := newTestBox(t, "a.example"), newTestBox(t, "b.example")
	a.shop("shop")
	raw := a.export("shop", exportOptions{withHistory: true})
	// Another project already serves shop's address here.
	m := changetest.M("other", func(m *manifest.Manifest) {
		m.Services = manifest.Services{}
		m.Apps = map[string]manifest.App{"web": {Path: ".", Framework: manifest.FrameworkBun, Role: manifest.RoleWeb, Routes: []string{"shop"}, Instances: 1}}
	})
	changetest.Converge(t, b.p.Engine, m)
	_, err := b.importArchive(raw, importOptions{})
	if err == nil || !strings.Contains(err.Error(), "already served") {
		t.Fatalf("route clash: %v", err)
	}
	if _, ok := b.b.dbs["shop"]; ok {
		t.Fatal("the failed import left its database")
	}
	if hist, _ := b.p.DB.ListChanges(ctx, change.ListFilter{Project: "shop"}); len(hist) != 0 {
		t.Fatalf("the failed import left History: %d", len(hist))
	}
	// A damaged archive is refused.
	bad := append([]byte(nil), raw...)
	bad[len(bad)/2] ^= 0xff
	if _, err := b.importArchive(bad, importOptions{name: "shop-x"}); err == nil {
		t.Fatal("a damaged archive was imported")
	}
	// A box export is not a project export.
	var buf bytes.Buffer
	w, _ := boxfile.NewWriter(&buf, &boxfile.Manifest{Kind: boxfile.ManifestKind, Format: boxfile.FormatVersion})
	_, _, _, _ = w.Close()
	if _, err := b.importArchive(buf.Bytes(), importOptions{}); err == nil || !strings.Contains(err.Error(), "tiffin box import") {
		t.Fatalf("box export: %v", err)
	}
}

// TestImportTrustsNoArchive: an archive cannot plant links, set-id files,
// git hooks or config, or reach another project's images.
func TestImportTrustsNoArchive(t *testing.T) {
	x := newTestBox(t, "a.example")
	x.shop("shop")
	media := x.b.bucketDir("shop", "media")
	if err := os.Symlink("/etc/passwd", filepath.Join(media, "passwd")); err != nil {
		t.Fatal(err)
	}
	site := x.b.releases["shop/site"].StaticRoot
	_ = os.WriteFile(filepath.Join(site, "run.sh"), []byte("#!/bin/sh"), 0o755)
	_ = os.Chmod(filepath.Join(site, "run.sh"), 0o755|os.ModeSetuid)
	git := x.b.gitDir("shop")
	_ = os.MkdirAll(filepath.Join(git, "hooks"), 0o755)
	_ = os.WriteFile(filepath.Join(git, "hooks", "pre-receive"), []byte("#!/bin/sh\nevil"), 0o755)
	_ = os.WriteFile(filepath.Join(git, "config"), []byte("[core]\n\thooksPath = /tmp/evil\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(git, "refs", "heads"), 0o755)
	_ = os.WriteFile(filepath.Join(git, "refs", "heads", "main"), []byte(strings.Repeat("a", 40)+"\n"), 0o644)

	if _, err := x.importArchive(x.export("shop", exportOptions{}), importOptions{name: "shop-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(x.b.bucketDir("shop-2", "media"), "passwd")); err == nil {
		t.Fatal("a symlink came in")
	}
	fi, err := os.Stat(filepath.Join(x.b.releases["shop-2/site"].StaticRoot, "run.sh"))
	if err != nil || fi.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("set-id bit kept: %v %v", fi.Mode(), err)
	}
	g2 := x.b.gitDir("shop-2")
	if _, err := os.Stat(filepath.Join(g2, "hooks", "pre-receive")); err == nil {
		t.Fatal("a git hook came in")
	}
	if cfg := readFile(t, filepath.Join(g2, "config")); strings.Contains(cfg, "hooksPath") || !strings.Contains(cfg, "receivepack = true") {
		t.Fatalf("git config: %s", cfg)
	}
	if readFile(t, filepath.Join(g2, "refs", "heads", "main")) != strings.Repeat("a", 40)+"\n" {
		t.Fatal("refs not copied")
	}

	// A release.json naming an image (no image.tar) only works for a
	// duplicate: an import cannot pick up any image the box has.
	res, err := x.importArchive(x.export("shop", exportOptions{sameBox: true}), importOptions{name: "shop-3"})
	if err != nil {
		t.Fatal(err)
	}
	if x.b.releases["shop-3/web"] != nil || !strings.Contains(strings.Join(res.notes, " "), "web's release was not in the archive") {
		t.Fatalf("an import tagged a box image: %+v", res)
	}
}

func TestRenamed(t *testing.T) {
	m := &manifest.Manifest{Project: "shop", Apps: map[string]manifest.App{
		"web":    {Role: manifest.RoleWeb, Routes: []string{"shop", "shop/api", "shop-admin", "www.example.com", "example.com/x"}},
		"blog":   {Role: manifest.RoleWeb},
		"custom": {Role: manifest.RoleWeb, Routes: []string{"example.org"}},
		"api":    {Role: manifest.RoleWeb, Routes: []string{"api"}}, // an address from before they were named after the project
		"site":   {Role: manifest.RoleWeb, Routes: []string{"www"}},
		"jobs":   {Role: manifest.RoleWorker},
	}, Domains: map[string]manifest.Domain{"example.com": {}}}
	notes := renamed(m, "shop", "shop-copy")
	for app, want := range map[string][]string{
		"web": {"shop-copy", "shop-copy/api", "shop-copy-admin"}, "blog": {"shop-copy-blog"}, "custom": {"shop-copy-custom"},
		"api": {"shop-copy-api"}, "site": {"www-shop-copy"}, "jobs": nil,
	} {
		if got := m.Apps[app].Routes; !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", app, got, want)
		}
	}
	if m.Project != "shop-copy" || m.Domains != nil || len(notes) != 1 || !strings.Contains(notes[0], "example.com, example.org, www.example.com") {
		t.Fatalf("project %s domains %v notes %v", m.Project, m.Domains, notes)
	}
}

func TestComposeAndReadme(t *testing.T) {
	x := newTestBox(t, "a.example")
	x.shop("shop")
	_, _, files := entries(t, x.export("shop", exportOptions{}))
	c := files["docker-compose.yml"]
	for _, want := range []string{"name: shop", "image: pgvector/pgvector:pg18", "./database.sql:/docker-entrypoint-initdb.d/01-database.sql:ro",
		"image: valkey/valkey:8", "image: minio/minio", "mc mirror --overwrite /files/media local/shop-media", "mc anonymous set download local/shop-media",
		"image: docker.io/tiffin/shop-web:dep_1 # docker load -i apps/web/image.tar", `DATABASE_URL: "postgresql://app:app@postgres:5432/app?sslmode=disable"`,
		`REDIS_URL: "redis://valkey:6379"`, `VALKEY_PREFIX: "p_shop:"`, `S3_BUCKET_MEDIA: "shop-media"`, `PRICE: "$$5"`, `PORT: "3000"`,
		"./apps/site/site:/usr/share/nginx/html:ro", "# secrets to set: API_KEY", "# jobs: no release was exported"} {
		if !strings.Contains(c, want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	if strings.Contains(c, "S3_BUCKET:") {
		t.Error("S3_BUCKET is only for a single bucket")
	}
	r := files["README.md"]
	for _, want := range []string{"# shop", "docker load -i apps/web/image.tar", "docker compose up", "`files/media/`", "tiffin projects import shop.tiffin --name shop-2"} {
		if !strings.Contains(r, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	plain := compose(&ProjectInfo{Project: "x", Secrets: SecretsInfo{Names: []string{"K"}, Plain: true}, Apps: []AppInfo{{Name: "web", Image: "img", ImageFile: true}}},
		&manifest.Manifest{Apps: map[string]manifest.App{"web": {Role: manifest.RoleWeb}}})
	if !strings.Contains(plain, "env_file: [.env]") || strings.Contains(plain, "postgres:") {
		t.Fatalf("plain compose:\n%s", plain)
	}
}

func TestDotenvRoundTrip(t *testing.T) {
	in := map[string]string{"A": "plain", "B": "multi\nline \"quoted\" \\ back", "C": ""}
	if out := parseDotenv(dotenv(in)); fmt.Sprint(out) != fmt.Sprint(in) {
		t.Fatalf("%v != %v", out, in)
	}
}
