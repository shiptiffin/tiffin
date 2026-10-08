package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/change"
)

// Images and what else a deploy leaves on the data disk.
//
// Every deploy's image is named imageRepo<project>-<app>:<deploy> in the
// tiffin namespace. gc keeps an environment's live build, its newest
// KeepImages rollback targets (previews none) and releases still draining
// or pinned for workflow runs; a failed deploy's image goes at once; a
// deleted preview's go with it. A deleted app keeps only its live build,
// for an undo, for deletedAppKeep; a destroyed project keeps nothing.
//
// The hourly sweep (sweepImages) catches whatever those miss: images of
// builds a restart or a timeout cut short, of projects destroyed before
// their cleanup removed images, a load's leftover digest names. Removing a
// name lets containerd's garbage collection drop the content and snapshots
// no other name holds (nerdctl rmi waits for it).

const (
	// imageRepo is where every deploy image lives (imageRef).
	imageRepo = "docker.io/tiffin/"
	// imageMinAge: the sweep never touches a name stored more recently.
	// Builds (30 minute limit), imports and a box import's load before
	// its restart all finish well within it.
	imageMinAge = 6 * time.Hour
	// deletedAppKeep is how long a deleted app's live build stays for an
	// undo, as long as its disk folders stay in the trash.
	deletedAppKeep = diskTrashKeep
	// sweepEveryTicks: the housekeeping loop ticks every 15 seconds.
	sweepEveryTicks = 240
	// sweepFirstTick: the first sweep, 5 minutes after start.
	sweepFirstTick = 20
)

// projectName is what a project (or app) folder must be called for the
// sweep to remove it.
var projectName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// managedImage reports whether the sweep may remove an image of this name:
// a deploy's, or a digest name a load left. Base and cache images (Bun's,
// anything pulled) are never touched.
func managedImage(name string) bool {
	return strings.HasPrefix(name, imageRepo) || isLoadDigestName(name)
}

// SweptImage is one image the sweep removed (or, dry, would remove).
type SweptImage struct {
	Name    string    `json:"name"`
	Digest  string    `json:"digest,omitempty"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size" doc:"Unpacked size, layers shared with other images included"`
	Why     string    `json:"why"`
	Err     string    `json:"error,omitempty"`
}

// SweepResult is what one sweep did.
type SweepResult struct {
	DryRun  bool         `json:"dryRun"`
	Removed []SweptImage `json:"removed"`
	Failed  []SweptImage `json:"failed,omitempty"`
	// Kept: managed images left, and why.
	Kept map[string]string `json:"kept"`
	// GoneProjects: destroyed projects whose leftovers (records, images,
	// folders) were removed; ExpiredApps: deleted apps (project/app) past
	// deletedAppKeep.
	GoneProjects []string `json:"goneProjects,omitempty"`
	ExpiredApps  []string `json:"expiredApps,omitempty"`
	// FreedBytes is the data disk space gained (0 in a dry run).
	FreedBytes int64 `json:"freedBytes"`
}

// sweepImages removes, at most hourly, the images nothing needs (see
// neededImages) and the leftovers of destroyed projects and of deleted apps
// past their undo window. dryRun changes nothing and reports what it would
// do. An image a container uses, or stored within imageMinAge, always stays.
func (r *rt) sweepImages(ctx context.Context, dryRun bool) (*SweepResult, error) {
	res := &SweepResult{DryRun: dryRun, Kept: map[string]string{}}
	before := freeBytes(r.opt.DataDir)
	// Images first: whatever a deploy stores after this list is not in it.
	imgs, err := r.eng.Images(ctx)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	inv, err := r.inventory(ctx)
	if err != nil {
		return nil, err
	}
	need := r.neededImages(ctx, inv)
	used, err := r.eng.UsedImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	res.GoneProjects, res.ExpiredApps = inv.gone, inv.expired
	if !dryRun {
		for _, p := range inv.gone {
			if err := r.forget(ctx, p, ""); err != nil {
				r.p.Log.Warn("runtime: forget a destroyed project's leftovers", "project", p, "err", err)
			}
		}
		for _, pa := range inv.expired {
			p, a, _ := strings.Cut(pa, "/")
			if err := r.forget(ctx, p, a); err != nil {
				r.p.Log.Warn("runtime: forget a deleted app", "project", p, "app", a, "err", err)
			}
		}
		if len(inv.gone)+len(inv.expired) > 0 {
			r.p.Log.Info("runtime: removed the leftovers of destroyed projects and deleted apps", "projects", inv.gone, "apps", inv.expired)
		}
	}
	now := time.Now()
	removed := map[string]bool{}
	for _, im := range imgs {
		if !managedImage(im.Name) {
			continue
		}
		name := imageKey(im.Name)
		switch {
		case need[name] != "":
			res.Kept[im.Name] = need[name]
			continue
		case used[im.Name] || used[name]:
			res.Kept[im.Name] = "a container uses it"
			continue
		case im.Created.IsZero() || now.Sub(im.Created) < imageMinAge:
			res.Kept[im.Name] = "stored less than " + imageMinAge.String() + " ago"
			continue
		}
		s := SweptImage{Name: im.Name, Digest: im.Digest, Created: im.Created, Size: im.Size, Why: inv.why(name, im.Name)}
		if !dryRun {
			// Under its own timeout: containerd's garbage collection runs before rmi returns.
			rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := r.eng.RemoveImage(rctx, im.Name)
			cancel()
			if err != nil && !noSuchImage(err) {
				s.Err = err.Error()
				res.Failed = append(res.Failed, s)
				r.p.Log.Warn("runtime: remove an unused image", "image", im.Name, "why", s.Why, "err", err)
				continue
			}
			removed[name] = true
			r.p.Log.Info("runtime: removed an unused image", "image", im.Name, "why", s.Why, "size", humanBytes(im.Size), "stored", im.Created.UTC().Format(time.RFC3339))
		}
		res.Removed = append(res.Removed, s)
	}
	// Records of apps that are still here forget the images they lost.
	forgot := false
	for _, d := range inv.deploys {
		if d.Image != "" && removed[imageKey(d.Image)] && inv.projects[d.Project] {
			if cur, err := r.st.getDeploy(ctx, d.Project, d.App, d.ID); err == nil && cur.Image == d.Image {
				cur.Image = ""
				_ = r.st.putDeploy(ctx, cur)
				forgot = true
			}
		}
	}
	if forgot {
		_ = r.refreshIfNeeded(ctx) // those versions' addresses now say they were cleaned up
	}
	if !dryRun {
		// The build cache, to its cap (the same one buildkitd.toml sets).
		if pr, ok := r.bld.(cachePruner); ok {
			if err := pr.PruneCache(ctx, buildCacheCap(diskBytes(r.opt.DataDir))); err != nil {
				r.p.Log.Warn("runtime: prune the build cache", "err", err)
			}
		}
		if after := freeBytes(r.opt.DataDir); before >= 0 && after > before {
			res.FreedBytes = after - before
		}
		if len(res.Removed)+len(res.Failed) > 0 || len(inv.gone)+len(inv.expired) > 0 {
			r.p.Log.Info("runtime: image sweep", "removed", len(res.Removed), "failed", len(res.Failed), "kept", len(res.Kept),
				"freed", humanBytes(res.FreedBytes))
		}
	}
	return res, nil
}

// imageKey is the name an image is stored under: deploy images normalized
// the way nerdctl reads a name, digest names exactly as they are.
func imageKey(name string) string {
	if isLoadDigestName(name) {
		return name
	}
	return dockerName(name)
}

// inventory is what the box knows of projects, apps and deploys, read once
// per sweep.
type inventory struct {
	projects map[string]bool      // existing projects
	apps     map[string]bool      // existing apps: "project/app"
	deploys  []*Deploy            // every record, every project, newest first
	states   map[string]*AppState // by envKey
	byImage  map[string]*Deploy   // image key → its deploy record
	gone     []string             // destroyed projects with leftovers
	expired  []string             // deleted apps past deletedAppKeep: "project/app"
	deleted  map[string]time.Time // deleted apps within it → when they stopped
	envs     map[string][]*Deploy // envKey → its deploys, newest first
}

func (r *rt) inventory(ctx context.Context) (*inventory, error) {
	inv := &inventory{projects: map[string]bool{}, apps: map[string]bool{}, states: map[string]*AppState{},
		byImage: map[string]*Deploy{}, deleted: map[string]time.Time{}, envs: map[string][]*Deploy{}}
	projects, err := r.p.DB.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	for _, pr := range projects {
		inv.projects[pr] = true
		_, res, err := r.p.DB.Load(ctx, pr)
		if err != nil {
			return nil, fmt.Errorf("load project %s: %w", pr, err)
		}
		for addr := range res {
			if change.Kind(addr) == change.KindApp {
				inv.apps[pr+"/"+change.Name(addr)] = true
			}
		}
	}
	if inv.deploys, err = r.st.allDeploys(ctx); err != nil {
		return nil, fmt.Errorf("list deploys: %w", err)
	}
	states, err := r.st.allStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list app states: %w", err)
	}
	for _, s := range states {
		inv.states[envKey(s.Project, s.App, s.Preview)] = s
	}
	gone := map[string]bool{}
	// A deleted app: when its environments stopped (zero: one still runs,
	// or a deploy is under way), or of its newest deploy without any.
	deletedAt := map[string]time.Time{}
	running := map[string]bool{}
	for _, d := range inv.deploys {
		k := envKey(d.Project, d.App, d.Preview)
		inv.envs[k] = append(inv.envs[k], d)
		if d.Image != "" {
			inv.byImage[imageKey(d.Image)] = d
		}
		pa := d.Project + "/" + d.App
		switch {
		case !inv.projects[d.Project]:
			gone[d.Project] = true
		case !inv.apps[pa]:
			if !d.Terminal() {
				running[pa] = true
			}
			if t := d.CreatedAt; t.After(deletedAt[pa]) {
				deletedAt[pa] = t
			}
		}
	}
	for _, s := range states {
		pa := s.Project + "/" + s.App
		switch {
		case !inv.projects[s.Project]:
			gone[s.Project] = true
		case !inv.apps[pa]:
			if !s.Stopped || len(s.Instances) > 0 {
				running[pa] = true
			}
			if s.UpdatedAt.After(deletedAt[pa]) {
				deletedAt[pa] = s.UpdatedAt
			}
		}
	}
	// Folders of projects with no records left (destroyed before records
	// were removed with them).
	for _, root := range []string{filepath.Join(r.opt.DataDir, "deploys"), filepath.Join(r.opt.DataDir, "static"),
		filepath.Join(r.opt.DataDir, "assets"), filepath.Join(r.opt.DataDir, "next-cache"), r.opt.LogDir} {
		es, _ := os.ReadDir(root)
		for _, e := range es {
			if e.IsDir() && projectName.MatchString(e.Name()) && !inv.projects[e.Name()] {
				gone[e.Name()] = true
			}
		}
	}
	for p := range gone {
		inv.gone = append(inv.gone, p)
	}
	sort.Strings(inv.gone)
	for pa, at := range deletedAt {
		switch {
		case running[pa]:
		case time.Since(at) > deletedAppKeep:
			inv.expired = append(inv.expired, pa)
		default:
			inv.deleted[pa] = at
		}
	}
	sort.Strings(inv.expired)
	return inv, nil
}

// neededImages maps the image names (imageKey) the box still needs to why:
// every unfinished deploy's (named before its build stores it), and per
// environment of an existing app what gc keeps (keptDeploys). A deleted
// app keeps its environments' live builds until deletedAppKeep; a
// destroyed project nothing.
func (r *rt) neededImages(ctx context.Context, inv *inventory) map[string]string {
	need := map[string]string{}
	for k, ds := range inv.envs {
		d0 := ds[0]
		project, app, preview := d0.Project, d0.App, d0.Preview
		pa := project + "/" + app
		for _, d := range ds {
			if !d.Terminal() {
				need[imageKey(imageRef(project, app, d.ID))] = "deploy " + d.ID + " is " + d.Status
				if d.Image != "" {
					need[imageKey(d.Image)] = "deploy " + d.ID + " is " + d.Status
				}
			}
		}
		switch {
		case !inv.projects[project]:
			continue
		case !inv.apps[pa]:
			at, ok := inv.deleted[pa]
			st := inv.states[k]
			if !ok || st == nil || st.Live == "" {
				continue
			}
			for _, d := range ds {
				if d.ID == st.Live && d.Image != "" {
					need[imageKey(d.Image)] = fmt.Sprintf("live build of deleted app %s, kept for an undo until %s", pa, at.Add(deletedAppKeep).UTC().Format(time.RFC3339))
				}
			}
			continue
		}
		rollbacks := r.rollbackTargets(preview)
		if preview == "" {
			if _, ok := r.pinnedReleases(ctx, project, app); !ok {
				rollbacks = len(ds) // no answer on workflow pins: keep every rollback target
			}
		}
		keep := r.keptDeploys(ctx, project, app, preview, ds, rollbacks)
		st := inv.states[k]
		for _, d := range ds {
			if !keep[d.ID] || d.Image == "" {
				continue
			}
			why := "rollback target of " + envKey(project, app, preview)
			switch {
			case !d.Terminal():
				continue // named above
			case st != nil && st.Live == d.ID:
				why = "live build of " + envKey(project, app, preview)
			case d.Status == StatusLive:
				why = "live build of " + envKey(project, app, preview)
			case !d.Rollbackable():
				why = "release draining or pinned for workflow runs"
			}
			need[imageKey(d.Image)] = why
		}
	}
	return need
}

// why says why an image is not needed, for the log and a dry run.
func (inv *inventory) why(key, name string) string {
	d := inv.byImage[key]
	if d == nil {
		if isLoadDigestName(name) {
			return "digest name a load left behind"
		}
		for _, p := range inv.gone {
			if strings.HasPrefix(name, imageRepo+p+"-") {
				return "no deploy record; named like destroyed project " + p
			}
		}
		return "no deploy record (a build or import that did not finish, or a destroyed project)"
	}
	pa := d.Project + "/" + d.App
	switch {
	case !inv.projects[d.Project]:
		return "project " + d.Project + " was destroyed (deploy " + d.ID + ")"
	case !inv.apps[pa]:
		return "app " + pa + " was deleted (deploy " + d.ID + ")"
	case d.Status == StatusFailed:
		return "deploy " + d.ID + " failed"
	case d.Preview != "":
		return "earlier build of preview " + d.Preview + " (deploy " + d.ID + ", " + d.Status + ")"
	}
	return "deploy " + d.ID + " (" + d.Status + ") is past the rollback targets kept"
}

// forget removes what the runtime keeps for a destroyed project (app "")
// or a deleted app past its undo window: its deploys' images and records,
// app states, work folders (source archives, build logs), static files,
// logs, client assets and image caches. Disk folders go to the trash.
// Containers were removed before (stopEnv, sweep).
func (r *rt) forget(ctx context.Context, project, app string) error {
	if !projectName.MatchString(project) || (app != "" && !projectName.MatchString(app)) {
		return fmt.Errorf("not a project or app name: %q/%q", project, app)
	}
	var ds []*Deploy
	var err error
	if app == "" {
		ds, err = r.st.listProjectDeploys(ctx, project)
	} else {
		ds, err = r.st.listDeploys(ctx, project, app, "*")
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range ds {
		if d.Image != "" {
			if err := r.eng.RemoveImage(ctx, d.Image); err != nil && !noSuchImage(err) {
				// The record goes anyway: the next sweep removes the image as unneeded.
				r.p.Log.Warn("runtime: remove an image", "image", d.Image, "err", err)
			}
		}
		if err := r.st.deleteDeploy(ctx, d); err != nil {
			errs = append(errs, err)
		}
	}
	states, err := r.st.allStates(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, s := range states {
		if s.Project == project && (app == "" || s.App == app) {
			if ins := slices.Concat(s.Instances, s.Parked); len(ins) > 0 {
				r.removeInstances(ctx, ins)
			}
			if err := r.st.deleteState(ctx, s); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, root := range []string{filepath.Join(r.opt.DataDir, "deploys"), filepath.Join(r.opt.DataDir, "static"), r.opt.LogDir} {
		dir := filepath.Join(root, project)
		if app != "" {
			dir = filepath.Join(dir, app)
		}
		r.removeDir(dir)
	}
	r.forgetFiles(project, app, "", true)
	r.trashDisks(project, app)
	r.mu.Lock()
	for k := range r.timeouts {
		if p, a, _ := strings.Cut(k, "/"); p == project && (app == "" || a == app) {
			delete(r.timeouts, k)
		}
	}
	r.mu.Unlock()
	return errors.Join(errs...)
}

// allDeploys is every deploy record on the box, newest first.
func (s store) allDeploys(ctx context.Context) ([]*Deploy, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT value FROM kv WHERE ns LIKE 'runtime/deploys/%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deploy{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var d Deploy
		if json.Unmarshal(b, &d) == nil && d.ID != "" {
			out = append(out, &d)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// diskBytes is the size of the file system holding path (0: unknown).
func diskBytes(path string) int64 {
	var st syscall.Statfs_t
	for p := path; ; p = filepath.Dir(p) {
		if err := syscall.Statfs(p, &st); err == nil {
			return int64(st.Blocks) * int64(st.Bsize)
		}
		if p == filepath.Dir(p) {
			return 0
		}
	}
}

// cachePruner trims BuildKit's cache to a size (the box's builder).
type cachePruner interface {
	PruneCache(ctx context.Context, maxBytes int64) error
}

// PruneCache removes the least recently used build cache until it holds
// at most maxBytes. BuildKit's own GC only runs after builds; the hourly
// sweep calls this so an idle box gives the space back too.
func (b *boxBuilder) PruneCache(ctx context.Context, maxBytes int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, b.tool("buildctl"), "prune", "--keep-storage", strconv.FormatInt(maxBytes/1_000_000, 10)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("buildctl prune: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// freeBytes is what the file system holding path has free (-1: unknown).
func freeBytes(path string) int64 {
	var st syscall.Statfs_t
	for p := path; ; p = filepath.Dir(p) {
		if err := syscall.Statfs(p, &st); err == nil {
			return int64(st.Bavail) * int64(st.Bsize)
		}
		if p == filepath.Dir(p) {
			return -1
		}
	}
}
