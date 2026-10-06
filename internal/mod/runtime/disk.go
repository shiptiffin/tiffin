package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/backup"
	"github.com/btahir/tiffin/internal/platform"
)

// An app's disk folders (App.Disk) live on the box under
// disks/<project>/<app>/<env>/<path>, env being prod or pr-<preview>, and
// every instance of the environment mounts them at <working dir>/<path>.
// A folder is made the first time an instance starts with it, from what
// the image has at that path; from then on it is the app's, kept across
// deploys, restarts and rollbacks. A preview's folders go with the
// preview. A deleted app's or project's go to disks-trash for diskTrashKeep.

const diskTrashKeep = 7 * 24 * time.Hour

// Box backups carry the folders, copied with reflinks while apps run.
func init() { backup.Include("app-disks", filepath.Join(DataDir, "disks")) }

func (r *rt) diskDir(project, app, preview string) string {
	return filepath.Join(r.opt.DataDir, "disks", project, app, envDirName(preview))
}

// diskMounts makes the environment's missing disk folders and returns the
// mounts for its instances.
func (r *rt) diskMounts(ctx context.Context, d *Deploy, spec *manifest.App, log io.Writer) ([]string, error) {
	if len(spec.Disk) == 0 || d.Image == "" {
		return nil, nil
	}
	workDir, user, err := r.eng.ImageConfig(ctx, d.Image)
	if err != nil {
		return nil, fmt.Errorf("read the image of deploy %s: %w", d.ID, err)
	}
	if workDir == "" {
		workDir = "/"
	}
	root := r.diskDir(d.Project, d.App, d.Preview)
	var missing []string
	made := map[string]bool{}
	for _, p := range spec.Disk.Paths() {
		if !exists(filepath.Join(root, p)) {
			missing = append(missing, p)
			made[p] = true
		}
	}
	if len(missing) > 0 {
		if err := r.seedDisks(ctx, d.Image, user, root, missing, log); err != nil {
			return nil, err
		}
	}
	if err := r.quotas.apply(ctx, d.Project, d.App, d.Preview, spec.Disk, made, log); err != nil {
		// The app still starts, its folders unlimited as they were.
		fmt.Fprintf(log, "==> warning: could not set the disk folders' sizes: %v\n", err)
		r.p.Log.Error("runtime: disk folder sizes", "project", d.Project, "app", d.App, "preview", d.Preview, "err", err)
	}
	mounts := make([]string, len(spec.Disk))
	for i, p := range spec.Disk.Paths() {
		mounts[i] = filepath.Join(root, p) + ":" + path.Join(workDir, p)
	}
	return mounts, nil
}

// seedDisks makes folders from the image, in a scratch directory first so a
// failed copy never leaves a half-made folder that would count as made.
func (r *rt) seedDisks(ctx context.Context, image, user, root string, paths []string, log io.Writer) error {
	tmp := root + ".seeding"
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := r.eng.SeedDirs(sctx, image, user, paths, tmp); err != nil {
		return &startError{msg: fmt.Sprintf("could not make the disk folders %s from the image: %v", strings.Join(paths, ", "), err)}
	}
	for i, p := range paths {
		from, to := filepath.Join(tmp, strconv.Itoa(i)), filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		n, size := countFiles(to)
		fmt.Fprintf(log, "==> disk folder %s: made, with %d files (%s) from the image; it keeps what the app writes from now on\n", p, n, humanBytes(size))
	}
	return nil
}

// trashDisks moves the disk folders of an app (app "": of every app of the
// project) to the trash, where they stay for diskTrashKeep.
func (r *rt) trashDisks(project, app string) {
	src := filepath.Join(r.opt.DataDir, "disks", project)
	name := project
	if app != "" {
		src = filepath.Join(src, app)
		name += "." + app
	}
	if !exists(src) {
		return
	}
	trash := filepath.Join(r.opt.DataDir, "disks-trash")
	dst := filepath.Join(trash, name+"."+time.Now().UTC().Format("20060102T150405"))
	if err := os.MkdirAll(trash, 0o700); err == nil {
		err = os.Rename(src, dst)
		if err == nil {
			r.p.Log.Info("runtime: disk folders moved to the trash", "project", project, "app", app, "path", dst, "keep", diskTrashKeep)
			prefix := project + "/"
			if app != "" {
				prefix += app + "/"
			}
			r.quotas.forget(r.ctx, prefix)
			forgetDiskBytes(project)
			return
		}
		r.p.Log.Error("runtime: move disk folders to the trash", "project", project, "app", app, "err", err)
	}
}

// emptyDiskTrash deletes trashed disk folders older than diskTrashKeep.
func (r *rt) emptyDiskTrash() {
	trash := filepath.Join(r.opt.DataDir, "disks-trash")
	es, _ := os.ReadDir(trash)
	for _, e := range es {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > diskTrashKeep {
			_ = os.RemoveAll(filepath.Join(trash, e.Name()))
		}
	}
}

// DiskDir is where a project's app keeps its production disk folders (each
// at its path below it), for exports and imports.
func (m *Module) DiskDir(project, app string) (string, error) {
	r, err := m.rt()
	if err != nil {
		return "", err
	}
	return r.diskDir(project, app, ""), nil
}

// diskUsage caches each project's disk folder bytes: a walk is quick for
// big files, slower for many small ones, and the disk guard asks often.
var diskUsage = struct {
	sync.Mutex
	root string
	at   map[string]time.Time
	n    map[string]int64
}{at: map[string]time.Time{}, n: map[string]int64{}}

func setDiskRoot(dir string) {
	diskUsage.Lock()
	diskUsage.root = dir
	diskUsage.Unlock()
}

func forgetDiskBytes(project string) {
	diskUsage.Lock()
	delete(diskUsage.at, project)
	diskUsage.Unlock()
	if q := currentQuotas(); q != nil {
		q.mu.Lock()
		q.used = nil
		q.mu.Unlock()
	}
}

// DiskBytes is the size of a project's app disk folders, production's and
// previews': from their quotas, or measured at most once a minute. They count toward the
// project's storage limit with its database and files.
func DiskBytes(project string) int64 {
	if n, ok := currentQuotas().projectBytes(context.Background(), project); ok {
		return n
	}
	diskUsage.Lock()
	root, at, n := diskUsage.root, diskUsage.at[project], diskUsage.n[project]
	diskUsage.Unlock()
	if root == "" || time.Since(at) < time.Minute {
		return n
	}
	n = dirSize(filepath.Join(root, project))
	diskUsage.Lock()
	diskUsage.at[project], diskUsage.n[project] = time.Now(), n
	diskUsage.Unlock()
	return n
}

// ProjectUsage reports a project's app disk folders for the usage API.
func (*Module) ProjectUsage(ctx context.Context, p *platform.Platform, project string) (*platform.ServiceUsage, error) {
	n := DiskBytes(project)
	if n == 0 {
		return nil, nil
	}
	return &platform.ServiceUsage{Service: "disk", Disk: "files", Bytes: n}, nil
}

// EstimateLoss says what deleting an app takes away: its disk folders (kept
// in the trash for a week).
func (m *Module) EstimateLoss(ctx context.Context, p *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	if change.Kind(op.Address) != change.KindApp || op.Action != change.Delete {
		return nil, nil
	}
	r, err := m.rt()
	if err != nil {
		return nil, nil
	}
	dir := filepath.Join(r.opt.DataDir, "disks", project, change.Name(op.Address))
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	n, size := countFiles(dir)
	if n == 0 {
		return nil, nil
	}
	return &change.Loss{Bytes: size, Counts: []change.LossCount{{N: int64(n), Unit: "file"}}}, nil
}
