package portable

// One project at a time: Duplicate (a copy on this box under a new name),
// Export (one project → a .tiffin archive of ordinary files a person can
// use without Tiffin) and Import (an archive → a new project beside the
// others; it never replaces one). A duplicate is an export streamed
// straight into an import, so the two share every step.
//
// A project archive holds, in this order (import reads it front to back):
//
//	tiffin-export.json      the manifest (kind tiffin-project-export)
//	project.json            what the project is: manifest, apps, buckets, secrets, extensions
//	README.md               how to run it without Tiffin
//	tiffin.config.ts        the project's config
//	docker-compose.yml      Postgres, Valkey, MinIO and the apps, with standard env vars
//	history/changes.jsonl   the project's History (--with-history)
//	secrets.json | .env     secrets sealed to the box key, or plain with --include-secrets
//	database-setup.sql      extensions and Tiffin's helper functions (runs first)
//	database.sql            pg_dump of the database: plain SQL, no owners or grants
//	cache.jsonl             the project's Valkey keys (DUMP payloads, base64)
//	files/<bucket>/...      every object, one file each (content types in xattrs)
//	source.git/             the push-to-deploy git repository, if the project has one
//	apps/<app>/release.json what the app runs (image, framework, commit)
//	apps/<app>/image.tar    the live image (docker load -i), for container apps
//	apps/<app>/site/...     the live files, for static sites
//	tiffin-export-end.json  the trailer (digest over everything)

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime"
	"github.com/btahir/tiffin/internal/mod/runtime/vercelcfg"
	"github.com/btahir/tiffin/internal/mod/storage"
)

// ProjectInfo is project.json: what an archive's project is.
type ProjectInfo struct {
	Project       string          `json:"project"`
	Source        boxfile.Source  `json:"source"`
	TiffinVersion string          `json:"tiffinVersion"`
	ExportedAt    time.Time       `json:"exportedAt"`
	Manifest      json.RawMessage `json:"manifest" doc:"The project's config as canonical JSON (what tiffin.config.ts evaluates to)"`
	StorageLimit  json.RawMessage `json:"storageLimit,omitempty"`
	Postgres      *PostgresInfo   `json:"postgres,omitempty"`
	Valkey        *ValkeyInfo     `json:"valkey,omitempty"`
	Buckets       []BucketInfo    `json:"buckets,omitempty"`
	Apps          []AppInfo       `json:"apps,omitempty"`
	Secrets       SecretsInfo     `json:"secrets"`
	Git           bool            `json:"git" doc:"source.git holds the project's push-to-deploy repository"`
	History       bool            `json:"history" doc:"history/changes.jsonl holds the project's History"`
}

// PostgresInfo describes database.sql.
type PostgresInfo struct {
	Database   string    `json:"database"`
	Role       string    `json:"role"`
	Extensions []string  `json:"extensions"`
	SizeBytes  int64     `json:"sizeBytes"`
	Cron       []cronJob `json:"cron,omitempty"`
}

// ValkeyInfo describes cache.jsonl.
type ValkeyInfo struct {
	Prefix string `json:"prefix" doc:"Every key's prefix on the box (VALKEY_PREFIX); cache.jsonl keys leave it out"`
}

// BucketInfo is one bucket under files/.
type BucketInfo struct {
	Name   string `json:"name"`
	S3Name string `json:"s3Name"`
	Public bool   `json:"public"`
}

// AppInfo is one app and the release it ran.
type AppInfo struct {
	Name      string `json:"name"`
	Framework string `json:"framework"`
	Role      string `json:"role"`
	URL       string `json:"url,omitempty"`
	Deploy    string `json:"deploy,omitempty" doc:"The live deploy exported (empty: the app had none)"`
	Image     string `json:"image,omitempty" doc:"The image reference (apps/<app>/image.tar holds it unless the archive stayed on the box)"`
	ImageFile bool   `json:"imageFile,omitempty"`
	Site      bool   `json:"site,omitempty" doc:"apps/<app>/site holds the static files"`
	Commit    string `json:"commit,omitempty"`
	Repo      string `json:"repo,omitempty"`
}

// SecretsInfo says how the archive carries secrets.
type SecretsInfo struct {
	Names     []string `json:"names"`
	Plain     bool     `json:"plain" doc:".env holds them in plain text"`
	Recipient string   `json:"recipient,omitempty" doc:"age public key secrets.json is sealed to (the source box's key)"`
}

// sealedSecrets is secrets.json.
type sealedSecrets struct {
	Recipient string                     `json:"recipient"`
	Note      string                     `json:"note"`
	Secrets   map[string]json.RawMessage `json:"secrets" doc:"name → {sealed, updatedAt, updatedBy}"`
}

// appRelease is apps/<app>/release.json.
type appRelease struct {
	App       string `json:"app"`
	Deploy    string `json:"deploy"`
	Framework string `json:"framework"`
	Image     string `json:"image,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Repo      string `json:"repo,omitempty"`
	// Dir and Vercel: the app's folder in its workspace, and what the
	// build took from its vercel.json (edge rules, crons).
	Dir    string            `json:"dir,omitempty"`
	Vercel *vercelcfg.Config `json:"vercel,omitempty"`
}

// cacheEntry is one line of cache.jsonl.
type cacheEntry struct {
	Key   string `json:"key" doc:"Without the project prefix"`
	TTLMs int64  `json:"ttlMs" doc:"0: no expiry"`
	Dump  []byte `json:"dump" doc:"The DUMP payload (RESTORE takes it)"`
}

// backend is what project exports read and imports write besides the
// platform state and files: the box's services (box.go), fakes in tests.
type backend interface {
	databaseInfo(ctx context.Context, project string) (*PostgresInfo, error) // nil without a database
	dumpDatabase(ctx context.Context, project string, w io.Writer) error
	prepareDatabase(ctx context.Context, project string, manifestExt, extra []string) error
	restoreDatabase(ctx context.Context, project string, r io.Reader) error
	dropDatabase(ctx context.Context, project string) error
	restoreCron(ctx context.Context, project string, jobs []cronJob) error
	dumpKeys(ctx context.Context, project string, each func(cacheEntry) error) error
	restoreKeys(ctx context.Context, project string, keys func(put func(cacheEntry) error) error) error
	liveRelease(ctx context.Context, project, app string) (*runtime.Deploy, error)
	saveImage(ctx context.Context, ref string, w io.Writer) error
	release(ctx context.Context, project, app string, src runtime.ReleaseSource, by string) (*runtime.Deploy, error)
	gitDir(project string) string
	bucketDir(project, bucket string) string
	// converged waits until the project's resources have converged.
	converged(ctx context.Context, project string) error
	// recentlyDeleted: a project of this name was destroyed lately, and
	// creating it again would bring its data back.
	recentlyDeleted(ctx context.Context, project string) bool
}

var projectName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// checkNewName says why name cannot be a new project here, or nil.
func checkNewName(ctx context.Context, b backend, existing []string, name string) error {
	if !projectName.MatchString(name) {
		return api.NewProblem(422, "validation", fmt.Sprintf("%q is not a project name: 1-40 lowercase letters, digits and dashes, starting with a letter", name))
	}
	if slices.Contains(existing, name) {
		prob := api.NewProblem(409, "conflict", "project "+name+" already exists on this box")
		prob.Hint = "pick another name (--name); an import never replaces a project"
		return prob
	}
	if b.recentlyDeleted(ctx, name) {
		prob := api.NewProblem(409, "conflict", "a project named "+name+" was destroyed less than 7 days ago; a new one of that name would get its old data back")
		prob.Hint = "pick another name (--name)"
		return prob
	}
	return nil
}

// renamed adapts a project's config to a new name on a box that may still
// run the original: names under the box domain move to the new project's
// own (shop → shop-copy, shop-api → shop-copy-api, www → www-shop-copy),
// an app served at its own app name (the default before addresses were
// named after the project) or only at custom domains gets the new project's
// default (shop-copy for its main app, shop-copy-<app> for the others), and
// custom domains and GitHub deploys stay with the original. It returns what
// it left behind.
func renamed(m *manifest.Manifest, from, to string) []string {
	var notes, domains, repos []string
	m.Project = to
	host := func(h string) string {
		switch {
		case h == from:
			return to
		case strings.HasPrefix(h, from+"-"):
			return to + strings.TrimPrefix(h, from)
		}
		return h + "-" + to
	}
	for name, a := range m.Apps {
		if a.Git != nil {
			repos = append(repos, a.Git.Repo)
			a.Git = nil
		}
		if a.Role == manifest.RoleWorker {
			m.Apps[name] = a
			continue
		}
		var out []string
		routes := a.Routes
		if len(routes) == 1 && routes[0] == name && name != from {
			routes = nil // the old default: the new project's default instead
		}
		for _, r := range routes {
			h, rest, hasPath := strings.Cut(r, "/")
			if strings.Contains(h, ".") {
				domains = append(domains, h)
				continue
			}
			nr := host(h)
			if hasPath {
				nr += "/" + rest
			}
			if !slices.Contains(out, nr) {
				out = append(out, nr)
			}
		}
		a.Routes = out // none left: Normalize gives the new project's default
		m.Apps[name] = a
	}
	manifest.Normalize(m)
	for d := range m.Domains {
		domains = append(domains, d)
	}
	m.Domains = nil
	slices.Sort(domains)
	if domains = slices.Compact(domains); len(domains) > 0 {
		notes = append(notes, "Custom domains stay with "+from+": "+strings.Join(domains, ", ")+".")
	}
	slices.Sort(repos)
	if repos = slices.Compact(repos); len(repos) > 0 {
		notes = append(notes, "GitHub deploys ("+strings.Join(repos, ", ")+") stay with "+from+"; connect "+to+" in its app settings if it should deploy too.")
	}
	return notes
}

// desiredResources is what the new project is created with: its config's
// resources (renamed if it has a new name), secrets and storage limit.
func desiredResources(info *ProjectInfo, name string, secrets map[string]json.RawMessage) (map[string]change.Resource, []string, error) {
	m, err := manifest.Parse(info.Manifest)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: project.json: %v", ErrInvalid, err)
	}
	var notes []string
	if name != info.Project {
		notes = renamed(m, info.Project, name)
	}
	if m.Services.Storage != nil {
		for b := range m.Services.Storage.Buckets {
			if s3 := storage.S3Name(name, b); len(s3) > 63 {
				return nil, nil, api.NewProblem(422, "validation", fmt.Sprintf("bucket %s would be %q here, longer than S3's 63 characters: pick a shorter project name", b, s3))
			}
		}
	}
	res, err := change.Resources(m)
	if err != nil {
		return nil, nil, err
	}
	for n, spec := range secrets {
		res[change.KindSecret+"/"+n] = change.Resource{Address: change.KindSecret + "/" + n, Spec: compactJSON(spec)}
	}
	if len(info.StorageLimit) > 0 {
		res[change.KindStorageLimit] = change.Resource{Address: change.KindStorageLimit, Spec: compactJSON(info.StorageLimit)}
	}
	return res, notes, nil
}

// compactJSON: resource specs are compact (archives hold them indented).
func compactJSON(raw json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return raw
	}
	return b.Bytes()
}

// spool copies r into a new file in dir and returns its path (archive
// entries need their size up front).
func spool(dir, name string, fill func(io.Writer) error) (string, error) {
	f, err := os.CreateTemp(dir, name+"-*")
	if err != nil {
		return "", err
	}
	err = fill(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

var errNotFoundProject = errors.New("no such project")
