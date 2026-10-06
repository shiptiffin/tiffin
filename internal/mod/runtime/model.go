package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/mod/runtime/vercelcfg"
	"github.com/btahir/tiffin/internal/state"
)

// Deploy statuses.
const (
	StatusQueued     = "queued"      // accepted, waiting for the builder
	StatusBuilding   = "building"    // Railpack + BuildKit (or a static/prebuilt import) running
	StatusStarting   = "starting"    // instances starting, waiting for health checks
	StatusLive       = "live"        // serving traffic
	StatusFailed     = "failed"      // build or health check failed; the previous deploy keeps serving
	StatusSuperseded = "superseded"  // a newer deploy went live; can be rolled back to
	StatusRolledBack = "rolled_back" // replaced by a rollback to an earlier deploy; can be rolled back to
	StatusStopped    = "stopped"     // was live, but its app or preview was deleted
	StatusSkipped    = "skipped"     // a newer push to the same branch or pull request arrived before it was built
)

// Sources of a deploy.
const (
	SourceUpload   = "upload"   // a gzipped tar of the source (tiffin deploy)
	SourceFiles    = "files"    // inline files in the request (agents)
	SourcePrebuilt = "prebuilt" // an image tarball built elsewhere
	SourceGit      = "git"      // git push to the box, or a clone of a public git URL (Repo set)
	SourceTemplate = "template" // a starter template embedded in tiffin
)

// Deploy is one build-and-release of an app (or of one of its previews).
type Deploy struct {
	ID          string     `json:"id" example:"dep_01JA2B3C4D5E6F7G8H9J0KMNPQ" doc:"Deploy ID"`
	Project     string     `json:"project"`
	App         string     `json:"app"`
	Preview     string     `json:"preview,omitempty" doc:"Preview name, empty for production"`
	Status      string     `json:"status" enum:"queued,building,starting,live,failed,superseded,rolled_back,stopped,skipped" doc:"queued → building → starting → live; failed keeps the previous deploy serving; superseded and rolled_back deploys can be rolled back to; skipped: a newer push arrived before it was built"`
	Source      string     `json:"source" enum:"upload,files,prebuilt,git,template"`
	Framework   string     `json:"framework,omitempty"`
	Commit      string     `json:"commit,omitempty" doc:"Git commit, for git pushes and deploys from a git URL or GitHub"`
	Repo        string     `json:"repo,omitempty" doc:"Repository URL, for deploys from a git URL or GitHub"`
	Ref         string     `json:"ref,omitempty" doc:"Branch, tag or commit asked for, for deploys from a git URL or GitHub"`
	Message     string     `json:"message,omitempty" doc:"The commit's message (first line), for deploys from GitHub"`
	Author      string     `json:"author,omitempty" doc:"Who made the commit (GitHub login or git author name), for deploys from GitHub"`
	PullRequest int        `json:"pullRequest,omitempty" doc:"The pull request a preview deploy is for, for deploys from GitHub"`
	Trigger     string     `json:"trigger,omitempty" enum:"push,pull_request,redeploy,env," doc:"What started a deploy from GitHub (a push to the production branch, a pull request, or a redeploy asked for on the box), or env: the box rebuilt the live version because env it builds into browser code changed"`
	Template    string     `json:"template,omitempty" doc:"Starter template, for template deploys"`
	Image       string     `json:"image,omitempty" doc:"Image reference in the box's containerd store"`
	Digest      string     `json:"digest,omitempty" doc:"Image manifest digest"`
	URL         string     `json:"url,omitempty" doc:"Where the deploy is served (web apps)"`
	Error       string     `json:"error,omitempty" doc:"Why the deploy failed"`
	Hint        string     `json:"hint,omitempty" doc:"What to do about the failure"`
	SourceBytes int64      `json:"sourceBytes,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	CreatedBy   string     `json:"createdBy,omitempty" doc:"Token that started the deploy"`
	BuiltAt     *time.Time `json:"builtAt,omitempty"`
	LiveAt      *time.Time `json:"liveAt,omitempty" doc:"When it went live: deployed, or rolled back to. Restarts and rescales don't move it."`
	FinishedAt  *time.Time `json:"finishedAt,omitempty" doc:"When it went live or failed"`
	BuildSecs   float64    `json:"buildSeconds,omitempty" doc:"Time spent building"`
	TotalSecs   float64    `json:"durationSeconds,omitempty" doc:"Queued to live (or failed)"`
	// StaticRoot is the directory a static deploy serves.
	StaticRoot string `json:"staticRoot,omitempty"`
	// Assets are the build's client-asset directories the box serves itself.
	Assets []AssetDir `json:"assets,omitempty" doc:"Client-asset directories of the build that the box serves itself (hashed files stay served for pages of earlier releases for a day)"`
	// Dir is the app's folder in its source when the source is a whole
	// workspace (a monorepo whose packages the app uses).
	Dir string `json:"dir,omitempty" doc:"The app's folder inside the uploaded source, when the source is the whole workspace (monorepo) the app builds in"`
	// Vercel is what the build took from the app's vercel.json.
	Vercel *vercelcfg.Config `json:"vercel,omitempty" doc:"What the deploy took from the app's vercel.json (build settings, crons, headers, redirects, rewrites) and what it ignored"`
	// PublicEnv identifies the browser-visible env the build had.
	PublicEnv   string  `json:"publicEnv,omitempty" doc:"Hash of the env the build wrote into browser code (NEXT_PUBLIC_*, VITE_*, PUBLIC_*). When it changes, the box rebuilds the app from this deploy's source."`
	ReleaseSecs float64 `json:"releaseSeconds,omitempty" doc:"Time the app's release command took"`
}

// Terminal reports whether the deploy finished its pipeline.
func (d *Deploy) Terminal() bool {
	switch d.Status {
	case StatusQueued, StatusBuilding, StatusStarting:
		return false
	}
	return true
}

// Rollbackable reports whether the deploy can be made live again.
func (d *Deploy) Rollbackable() bool {
	return (d.Status == StatusSuperseded || d.Status == StatusRolledBack || d.Status == StatusStopped) && (d.Image != "" || d.StaticRoot != "")
}

// Instance is one running container of a deploy.
type Instance struct {
	Name   string `json:"name" doc:"Container name"`
	Port   int    `json:"port" doc:"Localhost port the instance listens on"`
	Deploy string `json:"deploy"`
}

// AppState is what runs for one app environment (production or a preview).
type AppState struct {
	Project   string     `json:"project"`
	App       string     `json:"app"`
	Preview   string     `json:"preview,omitempty"`
	Live      string     `json:"live,omitempty"` // deploy ID serving
	Instances []Instance `json:"instances,omitempty"`
	// Hash of the config the instances were started with (env, memory, count...).
	Hash string `json:"hash,omitempty"`
	// Stopped: the app resource was deleted; Live is kept so undo can restore it.
	Stopped bool `json:"stopped,omitempty"`
	// Sleeping: a preview scaled to zero; the activator wakes it on a request.
	Sleeping bool `json:"sleeping,omitempty"`
	// Draining: earlier releases kept running (without routes) because
	// workflow runs are pinned to them; stopped once the queue lets go.
	Draining []DrainSet `json:"draining,omitempty"`
	// Retired: earlier releases, newest first, whose hashed client assets
	// are still served for pages loaded before they were replaced.
	Retired []Retired `json:"retired,omitempty"`
	// Serial makes container names unique across restarts.
	Serial    int       `json:"serial"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DrainSet is an old release's instances kept for pinned workflow runs.
type DrainSet struct {
	Release   string     `json:"release"`
	Instances []Instance `json:"instances"`
	Since     time.Time  `json:"since"`
}

// Retired is a release that stopped serving at At.
type Retired struct {
	Deploy string    `json:"deploy"`
	At     time.Time `json:"at"`
}

// envKey names an app environment: "project/app" or "project/app@preview".
func envKey(project, app, preview string) string {
	k := project + "/" + app
	if preview != "" {
		k += "@" + preview
	}
	return k
}

const (
	nsState = "runtime/state"
)

func nsDeploys(project, app string) string { return "runtime/deploys/" + project + "/" + app }

// store keeps deploy records and app states in the platform KV table.
type store struct {
	db    *state.DB
	cache *stateCache
}

var errNotFound = errors.New("not found")

func (s store) putDeploy(ctx context.Context, d *Deploy) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.db.KVPut(ctx, nsDeploys(d.Project, d.App), d.ID, b)
}

func (s store) getDeploy(ctx context.Context, project, app, id string) (*Deploy, error) {
	b, ok, err := s.db.KVGet(ctx, nsDeploys(project, app), id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound
	}
	var d Deploy
	return &d, json.Unmarshal(b, &d)
}

// listDeploys returns an app's deploys, newest first. preview "*" means all
// environments, "" production only.
func (s store) listDeploys(ctx context.Context, project, app, preview string) ([]*Deploy, error) {
	kv, err := s.db.KVList(ctx, nsDeploys(project, app))
	if err != nil {
		return nil, err
	}
	out := []*Deploy{}
	for _, b := range kv {
		var d Deploy
		if json.Unmarshal(b, &d) != nil {
			continue
		}
		if preview != "*" && d.Preview != preview {
			continue
		}
		out = append(out, &d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s store) deleteDeploy(ctx context.Context, d *Deploy) error {
	return s.db.KVDelete(ctx, nsDeploys(d.Project, d.App), d.ID)
}

func (s store) getState(ctx context.Context, project, app, preview string) (*AppState, error) {
	b, ok, err := s.db.KVGet(ctx, nsState, envKey(project, app, preview))
	if err != nil {
		return nil, err
	}
	if !ok {
		return &AppState{Project: project, App: app, Preview: preview}, nil
	}
	var st AppState
	return &st, json.Unmarshal(b, &st)
}

func (s store) putState(ctx context.Context, st *AppState) error {
	st.UpdatedAt = time.Now().UTC()
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := s.db.KVPut(ctx, nsState, envKey(st.Project, st.App, st.Preview), b); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.put(st)
	}
	return nil
}

func (s store) deleteState(ctx context.Context, st *AppState) error {
	if s.cache != nil {
		s.cache.del(envKey(st.Project, st.App, st.Preview))
	}
	return s.db.KVDelete(ctx, nsState, envKey(st.Project, st.App, st.Preview))
}

// allStates returns every app environment the box knows about.
func (s store) allStates(ctx context.Context) ([]*AppState, error) {
	kv, err := s.db.KVList(ctx, nsState)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*AppState, 0, len(kv))
	for _, k := range keys {
		var st AppState
		if json.Unmarshal(kv[k], &st) == nil {
			out = append(out, &st)
		}
	}
	return out, nil
}

// statesOf returns the production state and all preview states of an app.
func (s store) statesOf(ctx context.Context, project, app string) ([]*AppState, error) {
	all, err := s.allStates(ctx)
	if err != nil {
		return nil, err
	}
	var out []*AppState
	for _, st := range all {
		if st.Project == project && st.App == app {
			out = append(out, st)
		}
	}
	return out, nil
}

// imageRef is where a deploy's image lives in containerd.
func imageRef(project, app, id string) string {
	return "docker.io/tiffin/" + project + "-" + app + ":" + strings.ToLower(id)
}
