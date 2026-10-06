package runtime

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/storage"
	"github.com/btahir/tiffin/internal/platform"
)

// Disk folder sizes. Each folder of each environment (production, every
// preview) is an XFS project: its files carry the project's ID, which files
// made in it later inherit, and the project's hard block limit is the
// folder's size, so a write past it fails with "No space left on device"
// (ENOSPC). Growing a folder only raises the limit: no restart. While the
// disk guard holds the project read-only, the limit is what the folder
// holds, so it stops growing like the project's database and buckets.
//
// The data disk must be mounted with prjquota: Lima boxes and servers set
// up since are; a server set up before gets it in /etc/fstab on its next
// `tiffin up` and has it once it restarts. Until then folders are unlimited,
// as before, and health says why.

// quotaFS is the data disk's project quotas.
type quotaFS interface {
	// Assign gives dir and everything in it the project id; files made in
	// it later inherit it.
	Assign(dir string, id uint32) error
	// Limit sets project id's hard limit in bytes (0: none).
	Limit(id uint32, bytes int64) error
	// Usage returns the bytes each project uses.
	Usage() (map[uint32]int64, error)
}

// xfsQuota is quotaFS through xfs_quota (xfsprogs) on the filesystem
// mounted at mount.
type xfsQuota struct{ mount string }

func (q xfsQuota) run(cmd string) (string, error) {
	out, err := exec.Command("xfs_quota", "-x", "-c", cmd, q.mount).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("xfs_quota %q: %v: %s", cmd, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (q xfsQuota) Assign(dir string, id uint32) error {
	_, err := q.run(fmt.Sprintf("project -s -p %s %d", dir, id))
	return err
}

func (q xfsQuota) Limit(id uint32, n int64) error {
	_, err := q.run(fmt.Sprintf("limit -p bhard=%dk %d", (n+1023)>>10, id))
	return err
}

func (q xfsQuota) Usage() (map[uint32]int64, error) {
	out, err := q.run("report -p -n -N -b")
	if err != nil {
		return nil, err
	}
	return parseQuotaReport(out), nil
}

// parseQuotaReport reads `report -p -n -N -b` lines: "#<id> <used KiB> <soft> <hard> ...".
func parseQuotaReport(out string) map[uint32]int64 {
	used := map[uint32]int64{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[0], "#") {
			continue
		}
		id, err1 := strconv.ParseUint(f[0][1:], 10, 32)
		kib, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 == nil && err2 == nil {
			used[uint32(id)] = kib << 10
		}
	}
	return used
}

// detectQuota returns the data disk's project quotas for dir, or why there
// are none.
func detectQuota(dir string) (quotaFS, string) {
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, "this system has no XFS project quotas"
	}
	mount, fstype, opts := mountOf(string(raw), dir)
	fix := " Run `tiffin up` again (it mounts the data disk with project quotas from then on: prjquota in /etc/fstab), then restart the server (sudo reboot)."
	switch {
	case fstype != "xfs":
		return nil, fmt.Sprintf("%s is on %s, not XFS, so disk folder sizes are not enforced.", dir, cmp.Or(fstype, "an unknown filesystem"))
	case !strings.Contains(","+opts+",", ",prjquota,") && !strings.Contains(","+opts+",", ",pquota,"):
		return nil, "The data disk (" + mount + ") is mounted without project quotas, so disk folder sizes are not enforced." + fix
	}
	if _, err := exec.LookPath("xfs_quota"); err != nil {
		return nil, "xfs_quota is not installed (apt-get install xfsprogs), so disk folder sizes are not enforced."
	}
	return xfsQuota{mount: mount}, ""
}

// mountOf finds the mount holding dir in /proc/self/mounts: its mount
// point, filesystem type and options.
func mountOf(mounts, dir string) (mount, fstype, opts string) {
	for _, line := range strings.Split(mounts, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		mp := strings.ReplaceAll(f[1], `\040`, " ")
		if (dir == mp || mp == "/" || strings.HasPrefix(dir, mp+"/")) && len(mp) >= len(mount) {
			mount, fstype, opts = mp, f[2], f[3]
		}
	}
	return mount, fstype, opts
}

// kvStore is the platform database's key-value store.
type kvStore interface {
	KVGet(ctx context.Context, ns, key string) ([]byte, bool, error)
	KVPut(ctx context.Context, ns, key string, value []byte) error
	KVDelete(ctx context.Context, ns, key string) error
	KVList(ctx context.Context, ns string) (map[string][]byte, error)
}

const quotaNS = "disk-quota" // key <project>/<app>/<env>/<path> → folderQuota; "next" → the next ID

// folderQuota is one folder's XFS project.
type folderQuota struct {
	ID   uint32 `json:"id"`
	Size int64  `json:"size"` // its size as last applied
	// Floor: a folder that held more than its size when sizes came in keeps
	// room for what it held, plus 1 GB, until it is given a bigger size.
	Floor int64 `json:"floor,omitempty"`
}

func (f folderQuota) limit() int64 { return max(f.Size, f.Floor) }

// quotas keeps every disk folder to its size.
type quotas struct {
	fs   quotaFS // nil: no project quotas here
	why  string  // why fs is nil
	kv   kvStore
	root string // the disks directory
	now  func() time.Time

	mu     sync.Mutex
	set    map[uint32]int64 // limits set since the start
	used   map[uint32]int64
	usedAt time.Time
	held   map[string]bool // projects the disk guard holds read-only
	warned map[string]bool // folders past warnPercent, logged once
}

const warnPercent = 90

func newQuotas(fs quotaFS, why string, kv kvStore, root string) *quotas {
	return &quotas{fs: fs, why: why, kv: kv, root: root, now: time.Now,
		set: map[uint32]int64{}, held: map[string]bool{}, warned: map[string]bool{}}
}

// activeQuotas is the running runtime's, for the package-level calls of
// the disk guard and the usage API.
var activeQuotas struct {
	sync.Mutex
	q *quotas
}

func setActiveQuotas(q *quotas) {
	activeQuotas.Lock()
	activeQuotas.q = q
	activeQuotas.Unlock()
}

func currentQuotas() *quotas {
	activeQuotas.Lock()
	defer activeQuotas.Unlock()
	return activeQuotas.q
}

func folderKey(project, app, preview, path string) string {
	return project + "/" + app + "/" + envDirName(preview) + "/" + path
}

func (q *quotas) records(ctx context.Context) (map[string]folderQuota, error) {
	raw, err := q.kv.KVList(ctx, quotaNS)
	if err != nil {
		return nil, err
	}
	out := map[string]folderQuota{}
	for k, v := range raw {
		var f folderQuota
		if strings.Contains(k, "/") && json.Unmarshal(v, &f) == nil {
			out[k] = f
		}
	}
	return out, nil
}

func (q *quotas) put(ctx context.Context, key string, f folderQuota) error {
	b, _ := json.Marshal(f)
	return q.kv.KVPut(ctx, quotaNS, key, b)
}

// nextID hands out project IDs, never twice: a trashed folder's files keep
// theirs.
func (q *quotas) nextID(ctx context.Context) (uint32, error) {
	raw, _, err := q.kv.KVGet(ctx, quotaNS, "next")
	if err != nil {
		return 0, err
	}
	id := uint64(1000)
	if n, err := strconv.ParseUint(string(raw), 10, 32); err == nil && n > id {
		id = n
	}
	if err := q.kv.KVPut(ctx, quotaNS, "next", []byte(strconv.FormatUint(id+1, 10))); err != nil {
		return 0, err
	}
	return uint32(id), nil
}

// usage returns each project ID's bytes, read at most every 10 seconds
// (fresh: now).
func (q *quotas) usage(fresh bool) map[uint32]int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fs != nil && (fresh || q.used == nil || q.now().Sub(q.usedAt) > 10*time.Second) {
		if u, err := q.fs.Usage(); err == nil {
			q.used, q.usedAt = u, q.now()
		}
	}
	return q.used
}

// apply holds an environment's existing folders to their sizes, giving a
// folder its project the first time. made lists folders just made from the
// image (they get no floor).
func (q *quotas) apply(ctx context.Context, project, app, preview string, disk manifest.Disk, made map[string]bool, log io.Writer) error {
	if q == nil || q.fs == nil || len(disk) == 0 {
		return nil
	}
	recs, err := q.records(ctx)
	if err != nil {
		return err
	}
	env := filepath.Join(q.root, project, app, envDirName(preview))
	for _, d := range disk {
		dir := filepath.Join(env, d.Path)
		if !exists(dir) {
			continue
		}
		key := folderKey(project, app, preview, d.Path)
		rec, ok := recs[key]
		if !ok {
			id, err := q.nextID(ctx)
			if err != nil {
				return err
			}
			if err := q.fs.Assign(dir, id); err != nil {
				return fmt.Errorf("disk folder %s: %w", d.Path, err)
			}
			rec.ID = id
			if used := q.usage(true)[id]; used > d.Bytes() {
				if made[d.Path] {
					fmt.Fprintf(log, "==> warning: disk folder %s holds %s from the image, more than its size (%s): writes to it fail until it is bigger\n", d.Path, humanBytes(used), humanBytes(d.Bytes()))
				} else {
					rec.Floor = (used>>30 + 2) << 30
					fmt.Fprintf(log, "==> warning: disk folder %s holds %s, more than its size (%s): it may hold up to %s until you give it a size in tiffin.config.ts, e.g. disk: { %q: \"%dGB\" }\n",
						d.Path, humanBytes(used), humanBytes(d.Bytes()), humanBytes(rec.Floor), d.Path, rec.Floor>>30+1)
				}
			}
		}
		if rec.Size != d.Bytes() || !ok {
			if ok && rec.Floor > 0 && d.Bytes() >= q.usage(true)[rec.ID] {
				rec.Floor = 0 // given a size it fits in: that is its size now
			}
			rec.Size = d.Bytes()
			if err := q.put(ctx, key, rec); err != nil {
				return err
			}
		}
		if err := q.limit(project, rec); err != nil {
			return fmt.Errorf("disk folder %s: %w", d.Path, err)
		}
	}
	return nil
}

// limit sets a folder's limit: its size, or what it holds while the
// project is held read-only.
func (q *quotas) limit(project string, rec folderQuota) error {
	n := rec.limit()
	q.mu.Lock()
	held := q.held[project]
	q.mu.Unlock()
	if held {
		n = min(n, max(q.usage(true)[rec.ID], 4096))
	}
	q.mu.Lock()
	same := q.set[rec.ID] == n
	q.mu.Unlock()
	if same {
		return nil
	}
	if err := q.fs.Limit(rec.ID, n); err != nil {
		return err
	}
	q.mu.Lock()
	q.set[rec.ID] = n
	q.mu.Unlock()
	return nil
}

// hold freezes (or frees) a project's folders.
func (q *quotas) hold(ctx context.Context, project string, on bool) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	q.held[project] = on
	if !on {
		delete(q.held, project)
	}
	q.mu.Unlock()
	if q.fs == nil {
		return nil
	}
	recs, err := q.records(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for k, rec := range recs {
		if strings.HasPrefix(k, project+"/") {
			if err := q.limit(project, rec); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// forget drops the records of the folders under prefix (a project, an app
// or an environment, as "<project>/" or "<project>/<app>/..."), whose files
// are gone or in the trash.
func (q *quotas) forget(ctx context.Context, prefix string) {
	if q == nil {
		return
	}
	recs, err := q.records(ctx)
	if err != nil {
		return
	}
	for k, rec := range recs {
		if strings.HasPrefix(k, prefix) {
			_ = q.kv.KVDelete(ctx, quotaNS, k)
			if q.fs != nil {
				_ = q.fs.Limit(rec.ID, 0)
			}
			q.mu.Lock()
			delete(q.set, rec.ID)
			q.mu.Unlock()
		}
	}
}

// DiskFolder is one disk folder against its size.
type DiskFolder struct {
	App       string `json:"app"`
	Preview   string `json:"preview,omitempty"`
	Path      string `json:"path"`
	UsedBytes int64  `json:"usedBytes"`
	SizeBytes int64  `json:"sizeBytes" doc:"Its size: writes past it fail with \"disk full\" (ENOSPC). A folder that held more than its size when sizes came in may hold what it held plus 1 GB until it is given a bigger one."`
	Enforced  bool   `json:"enforced" doc:"False when the box's data disk has no project quotas (box health says why): the size is then not enforced"`
}

func (q *quotas) folders(ctx context.Context, project string) []DiskFolder {
	if q == nil || q.fs == nil {
		return nil
	}
	recs, err := q.records(ctx)
	if err != nil {
		return nil
	}
	used := q.usage(false)
	var out []DiskFolder
	for k, rec := range recs {
		parts := strings.SplitN(k, "/", 4)
		if len(parts) < 4 || parts[0] != project {
			continue
		}
		f := DiskFolder{App: parts[1], Path: parts[3], UsedBytes: used[rec.ID], SizeBytes: rec.limit(), Enforced: true}
		if p, ok := strings.CutPrefix(parts[2], "pr-"); ok {
			f.Preview = p
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.App != b.App {
			return a.App < b.App
		}
		if a.Preview != b.Preview {
			return a.Preview < b.Preview
		}
		return a.Path < b.Path
	})
	return out
}

// projectBytes is what a project's folders hold, from the quotas.
func (q *quotas) projectBytes(ctx context.Context, project string) (int64, bool) {
	if q == nil || q.fs == nil {
		return 0, false
	}
	var n int64
	for _, f := range q.folders(ctx, project) {
		n += f.UsedBytes
	}
	return n, true
}

// full returns the folders at warnPercent of their size or more, logging
// each once as it gets there.
func (q *quotas) full(ctx context.Context, logf func(msg string, args ...any)) []string {
	if q == nil || q.fs == nil {
		return nil
	}
	recs, err := q.records(ctx)
	if err != nil {
		return nil
	}
	used := q.usage(false)
	var out []string
	seen := map[string]bool{}
	for k, rec := range recs {
		lim, n := rec.limit(), used[rec.ID]
		if lim <= 0 || n*100 < lim*warnPercent {
			continue
		}
		seen[k] = true
		out = append(out, fmt.Sprintf("%s (%s of %s)", k, humanBytes(n), humanBytes(lim)))
		if logf == nil {
			continue
		}
		q.mu.Lock()
		first := !q.warned[k]
		q.warned[k] = true
		q.mu.Unlock()
		if first {
			logf("runtime: disk folder nearly full", "folder", k, "used", humanBytes(n), "size", humanBytes(lim))
		}
	}
	if logf != nil {
		q.mu.Lock()
		for k := range q.warned {
			if !seen[k] {
				delete(q.warned, k)
			}
		}
		q.mu.Unlock()
	}
	sort.Strings(out)
	return out
}

// DiskFolders reports a project's disk folders against their sizes (nil
// without project quotas).
func DiskFolders(ctx context.Context, project string) []DiskFolder {
	return currentQuotas().folders(ctx, project)
}

// HoldDisks freezes a project's disk folders at what they hold while the
// disk guard holds it read-only (on false: back to their sizes).
func HoldDisks(ctx context.Context, project string, on bool) error {
	return currentQuotas().hold(ctx, project, on)
}

// syncQuotas holds every live environment's folders to their sizes: after
// an upgrade, folders made before sizes existed get theirs here.
func (r *rt) syncQuotas(ctx context.Context) {
	q := r.quotas
	if q == nil || q.fs == nil {
		return
	}
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	specs := map[string]*manifest.App{}
	for _, st := range states {
		if st.Live == "" || st.Stopped {
			continue
		}
		k := st.Project + "/" + st.App
		spec, ok := specs[k]
		if !ok {
			spec, _ = r.appSpec(ctx, st.Project, st.App)
			specs[k] = spec
		}
		if spec == nil {
			continue
		}
		w := logWriter{log: r.p.Log, args: []any{"project", st.Project, "app", st.App, "preview", st.Preview}}
		if err := q.apply(ctx, st.Project, st.App, st.Preview, spec.Disk, nil, w); err != nil {
			r.p.Log.Error("runtime: disk folder sizes", "project", st.Project, "app", st.App, "preview", st.Preview, "err", err)
		}
	}
	q.full(ctx, r.p.Log.Warn)
}

// diskCheck is the health check of disk folder sizes: folders nearly full,
// or sizes not enforced (nil when no app has disk folders).
func (r *rt) diskCheck(ctx context.Context) *platform.Check {
	q := r.quotas
	if q == nil {
		return nil
	}
	if q.fs == nil {
		es, _ := os.ReadDir(q.root)
		if len(es) == 0 {
			return nil
		}
		return &platform.Check{Name: "disk folders", OK: false, Detail: q.why}
	}
	if full := q.full(ctx, nil); len(full) > 0 {
		return &platform.Check{Name: "disk folders", OK: false, Detail: fmt.Sprintf("at %d%% of their size or more: %s. Writes past a folder's size fail with \"disk full\": "+
			"delete files, or give it a bigger size in tiffin.config.ts (disk: { data: \"5GB\" }; it applies without a restart).", warnPercent, strings.Join(full, ", "))}
	}
	return nil
}

// logWriter logs each line written to it as a warning.
type logWriter struct {
	log  *slog.Logger
	args []any
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		w.log.Warn("runtime: "+strings.TrimPrefix(line, "==> warning: "), w.args...)
	}
	return len(p), nil
}

// checkDisks refuses disk folder sizes that cannot work: production's
// together over the project's storage limit (which its database and buckets
// share), or a folder made smaller than what it holds. Folders listed
// without a size (1GB) are left out of the sum, so manifests from before
// sizes keep working under a smaller limit; reaching the limit still holds
// the project, folders included.
func checkDisks(ctx context.Context, p *platform.Platform, project string, desired map[string]change.Resource) error {
	apps := map[string]manifest.App{}
	var sum int64
	for addr, res := range desired {
		if change.Kind(addr) != change.KindApp {
			continue
		}
		var a manifest.App
		if json.Unmarshal(res.Spec, &a) != nil || len(a.Disk) == 0 {
			continue
		}
		apps[change.Name(addr)] = a
		for _, f := range a.Disk {
			if f.Size != "" {
				sum += f.Bytes()
			}
		}
	}
	if len(apps) == 0 {
		return nil
	}
	limit, _, err := storage.Limit(ctx, p, project)
	if err != nil {
		return err
	}
	if res, ok := desired[change.KindStorageLimit]; ok {
		var s struct {
			MaxBytes int64 `json:"maxBytes"`
		}
		if json.Unmarshal(res.Spec, &s) == nil {
			limit = max(s.MaxBytes, 0)
		}
	}
	if limit > 0 && sum > limit {
		prob := api.NewProblem(422, "validation", fmt.Sprintf("the disk folders' sizes add up to %s, more than project %s's storage limit of %s (which its database and files share)",
			humanBytes(sum), project, humanBytes(limit)))
		prob.Hint = fmt.Sprintf("Give the folders smaller sizes in tiffin.config.ts (disk: { data: \"500MB\" }), or raise the limit: tiffin storage quota set %s --max-bytes N.", project)
		return prob
	}
	q := currentQuotas()
	if q == nil || q.fs == nil {
		return nil
	}
	recs, err := q.records(ctx)
	if err != nil {
		return err
	}
	used := q.usage(false)
	for _, name := range slices.Sorted(maps.Keys(apps)) {
		for _, f := range apps[name].Disk {
			for k, rec := range recs {
				// Production's and every preview's copy of the folder.
				parts := strings.SplitN(k, "/", 4)
				if len(parts) < 4 || parts[0] != project || parts[1] != name || parts[3] != f.Path {
					continue
				}
				if n := used[rec.ID]; f.Bytes() < rec.Size && f.Bytes() < n {
					where := "production"
					if pv, ok := strings.CutPrefix(parts[2], "pr-"); ok {
						where = "preview " + pv
					}
					prob := api.NewProblem(422, "validation", fmt.Sprintf("app %s's disk folder %s holds %s in %s, more than the %s asked: a folder cannot shrink below what it holds",
						name, f.Path, humanBytes(n), where, humanBytes(f.Bytes())))
					prob.Hint = fmt.Sprintf("Delete files from it first, or keep it at least %s.", humanBytes(n))
					prob.Errors = append(prob.Errors, api.FieldError{Path: "/apps/" + name + "/disk/" + f.Path, Message: "smaller than what it holds"})
					return prob
				}
			}
		}
	}
	return nil
}
