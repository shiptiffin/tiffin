package box

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/mod/runtime"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// DiskReport is what takes the data disk: by part of the box and by project.
type DiskReport struct {
	MeasuredAt time.Time     `json:"measuredAt" doc:"When it was measured (cached for a minute: it walks the data folders)"`
	Disk       Disk          `json:"disk" doc:"The data disk"`
	Parts      []DiskPart    `json:"parts" doc:"The data disk by part of the box, biggest first"`
	Projects   []ProjectDisk `json:"projects" doc:"Each project's share, biggest first"`
	// What the hourly sweep removes.
	UnusedImages     int   `json:"unusedImages" doc:"Images nothing needs (destroyed projects', builds past the rollback targets, failed or interrupted builds); the hourly sweep removes them once they are 6 hours old"`
	UnusedImageBytes int64 `json:"unusedImageBytes" doc:"Their unpacked size (shared layers counted in each)"`
}

// DiskPart is one part of the box on the data disk.
type DiskPart struct {
	Name   string `json:"name" doc:"The folder under the data disk"`
	What   string `json:"what"`
	Bytes  int64  `json:"bytes"`
	Detail string `json:"detail,omitempty"`
}

// ProjectDisk is one project's share of the data disk.
type ProjectDisk struct {
	Project       string `json:"project"`
	Exists        bool   `json:"exists" doc:"False: leftovers of a destroyed project, which the hourly sweep removes"`
	TotalBytes    int64  `json:"totalBytes" doc:"The sum of the parts below"`
	Images        int    `json:"images" doc:"Images of its apps: live builds and rollback targets (3 per app by default; previews keep only their live build)"`
	ImageBytes    int64  `json:"imageBytes" doc:"Their unpacked size; layers shared with other images (the same base) count in each"`
	DatabaseBytes int64  `json:"databaseBytes" doc:"Its Postgres databases"`
	FilesBytes    int64  `json:"filesBytes" doc:"Its buckets and its apps' disk folders"`
	KVBytes       int64  `json:"kvBytes" doc:"Its Valkey keys (memory, saved to disk)"`
	LogBytes      int64  `json:"logBytes" doc:"Its apps' container logs and build output (the log store keeps searchable copies for 30 days by default, counted under observe)"`
	BuildBytes    int64  `json:"buildBytes" doc:"Deploy work folders (source archives, build logs), static sites, client assets and image caches"`
	BackupBytes   int64  `json:"backupBytes" doc:"Database snapshots taken before restores and on destroy, kept 7 days (whole-box backups are under backups)"`
}

// diskParts names the data disk's folders.
var diskParts = map[string]string{
	"containerd": "App images and the build cache (containerd)",
	"buildkit":   "Build cache bookkeeping (BuildKit)",
	"postgres":   "Databases (Postgres)",
	"backups":    "Box backups, pgBackRest and database snapshots",
	"logs":       "App container logs and the edge's access log",
	"observe":    "Log, metric and trace stores",
	"runtime":    "Deploy work folders, static sites, client assets and apps' disk folders",
	"storage":    "Buckets",
	"valkey":     "KV (Valkey)",
	"auth":       "Auth (users, sessions)",
	"email":      "Email (sent mail log, queue)",
	"analytics":  "Web analytics",
	"cache":      "Downloaded tools",
	"trash":      "Deleted buckets' trash",
	"platform":   "Platform state (projects, changes, secrets)",
}

// diskCache keeps the last report: measuring walks the data folders.
type diskCache struct {
	mu   sync.Mutex
	last *DiskReport
}

const diskReportFor = time.Minute

func (m *Module) registerDisk(a huma.API) {
	op := api.Op("box-disk", http.MethodGet, "/v1/box/disk", "box disk", api.RiskRead, "Show what takes the data disk",
		"What fills the data disk, measured now (cached for a minute): the disk's size and use; each part of the box (images and build "+
			"cache, databases, backups, logs, the log/metric/trace stores, deploy files, buckets...); and each project's share: its images "+
			"(live builds and rollback targets), databases, files (buckets and disk folders), KV, logs, build files and database snapshots. "+
			"Projects marked exists: false are leftovers of destroyed ones that the hourly sweep removes, as it removes the unused images "+
			"counted here. Box only: a laptop running tiffin serve without --box answers 503.", "system")
	op.Errors = append(op.Errors, 503)
	huma.Register(a, op, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *DiskReport }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		s := m.sampler()
		if s == nil {
			p := api.NewProblem(503, "internal", "the disk is measured on a box; this server runs without --box")
			p.Hint = "Point the CLI or dashboard at a box (tiffin up), where tiffin serve runs with --box."
			return nil, p
		}
		return &struct{ Body *DiskReport }{m.disk.get(ctx, s)}, nil
	}))
}

func (c *diskCache) get(ctx context.Context, s *sampler) *DiskReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last != nil && time.Since(c.last.MeasuredAt) < diskReportFor {
		return c.last
	}
	c.last = measureDisk(ctx, s)
	return c.last
}

func measureDisk(ctx context.Context, s *sampler) *DiskReport {
	rep := &DiskReport{MeasuredAt: time.Now().UTC(), Disk: disk(s.dataMount), Parts: []DiskPart{}, Projects: []ProjectDisk{}}
	var rd *runtime.RuntimeDisk
	for _, mod := range platform.Modules() {
		if rm, ok := mod.(*runtime.Module); ok {
			rd, _ = rm.DiskUse(ctx)
		}
	}
	// The parts: every folder of the data disk, containerd as what the
	// others leave of the used space (walking image layers is slow).
	es, _ := os.ReadDir(s.dataMount)
	var others int64
	hasContainerd := false
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "containerd" {
			hasContainerd = true
			continue
		}
		n := treeSize(filepath.Join(s.dataMount, e.Name()))
		others += n
		what := diskParts[e.Name()]
		if what == "" {
			what = "Other"
		}
		rep.Parts = append(rep.Parts, DiskPart{Name: e.Name(), What: what, Bytes: n})
	}
	if hasContainerd {
		p := DiskPart{Name: "containerd", What: diskParts["containerd"], Bytes: max(0, int64(rep.Disk.UsedBytes)-others)}
		if rd != nil {
			p.Detail = "images " + humanSize(rd.ImagesBytes) + ", build cache " + humanSize(rd.BuildCacheBytes) +
				" (the build cache keeps itself under 8 GiB, past which BuildKit drops its least recently used steps)"
		}
		rep.Parts = append(rep.Parts, p)
	}
	sort.Slice(rep.Parts, func(i, j int) bool { return rep.Parts[i].Bytes > rep.Parts[j].Bytes })

	projects := map[string]*ProjectDisk{}
	of := func(name string) *ProjectDisk {
		if projects[name] == nil {
			projects[name] = &ProjectDisk{Project: name}
		}
		return projects[name]
	}
	if s.track != nil && s.track.p != nil {
		if ps, err := s.track.p.DB.ListProjects(ctx); err == nil {
			for _, name := range ps {
				pd := of(name)
				pd.Exists = true
				svcs, _ := s.track.services(ctx, name, 3*time.Second)
				for _, sv := range svcs {
					switch sv.Service {
					case "postgres":
						pd.DatabaseBytes += sv.Bytes
					case "storage", "disk":
						pd.FilesBytes += sv.Bytes
					case "valkey":
						pd.KVBytes += sv.Bytes
					}
				}
			}
		}
	}
	if rd != nil {
		for name, u := range rd.Projects {
			pd := of(name)
			pd.Images, pd.ImageBytes, pd.LogBytes, pd.BuildBytes = u.Images, u.ImageBytes, u.LogBytes, u.BuildBytes
		}
		rep.UnusedImages, rep.UnusedImageBytes = rd.UnusedImages, rd.UnusedImageBytes
	}
	snaps, _ := os.ReadDir(postgres.SnapshotDir)
	for _, e := range snaps {
		if e.IsDir() {
			of(e.Name()).BackupBytes = treeSize(filepath.Join(postgres.SnapshotDir, e.Name()))
		}
	}
	for _, pd := range projects {
		pd.TotalBytes = pd.ImageBytes + pd.DatabaseBytes + pd.FilesBytes + pd.KVBytes + pd.LogBytes + pd.BuildBytes + pd.BackupBytes
		if pd.TotalBytes > 0 || pd.Exists {
			rep.Projects = append(rep.Projects, *pd)
		}
	}
	sort.Slice(rep.Projects, func(i, j int) bool {
		if rep.Projects[i].TotalBytes != rep.Projects[j].TotalBytes {
			return rep.Projects[i].TotalBytes > rep.Projects[j].TotalBytes
		}
		return rep.Projects[i].Project < rep.Projects[j].Project
	})
	return rep
}

// treeSize adds up the regular files under dir (links not followed).
func treeSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func humanSize(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.1f kB", float64(n)/1e3)
}
