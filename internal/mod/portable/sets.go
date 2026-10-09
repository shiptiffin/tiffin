package portable

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// fileSet is one directory tree an archive carries, under files/<Name>/
// (or history/<Name>/).
type fileSet struct {
	Name   string
	Path   func(p *platform.Platform) string
	Detail string
	// History sets (logs, metrics, build logs) only travel with --with-history.
	History bool
	// Live sets are read in place instead of being copied inside the pause
	// (logs and metrics stores: append-only and large).
	Live bool
	// Skip leaves paths out of the archive (box-local or regenerated files).
	Skip func(rel string, d fs.DirEntry, withHistory bool) bool
	// Keep lists entries of the importing box's directory that survive the
	// swap (the same box-local files Skip leaves out).
	Keep func(withHistory bool) []string
	// SQLite marks sets holding Tiffin's own SQLite databases (snapshotted
	// with VACUUM INTO, never copied file by file).
	SQLite bool
	// Units are stopped while the set is swapped in.
	Units func(withHistory bool) []string
	// Restart are units restarted once the set is swapped in: they keep
	// what they read in memory, but stay up for the swap.
	Restart []string
}

func under(rel string) func(*platform.Platform) string {
	return func(p *platform.Platform) string {
		root := "/var/lib/tiffin"
		if p != nil && p.DataRoot != "" {
			root = p.DataRoot
		}
		return filepath.Join(root, rel)
	}
}

// observeStores are the Victoria stores in /var/lib/tiffin/observe.
var observeStores = []string{"metrics", "logs"}

// sets lists every file tree, in archive order.
func sets() []fileSet {
	return []fileSet{
		{Name: "edge", Detail: "HTTPS root CA and certificates",
			Path: func(p *platform.Platform) string {
				home := "/var/lib/tiffin/platform"
				if p != nil && p.Home != "" {
					home = p.Home
				}
				return filepath.Join(home, "edge")
			},
			Skip: func(rel string, _ fs.DirEntry, _ bool) bool { return rel == "locks" },
			Keep: func(bool) []string { return []string{"locks"} },
			// The edge process holds this box's CA and certificates in
			// memory; its socket keeps connections waiting while it restarts.
			Restart: []string{"tiffin-edge.service"}},
		{Name: "storage", Path: under("storage"), Detail: "buckets, objects, S3 accounts, audit manifests",
			Units: func(bool) []string { return []string{"tiffin-storage.service"} }},
		{Name: "storage-trash", Path: under("trash/storage"), Detail: "deleted buckets, restorable for 7 days"},
		{Name: "email", Path: under("email"), Detail: "dev inbox and outgoing mail"},
		{Name: "analytics", Path: under("analytics"), Detail: "visitors, pageviews and events", SQLite: true,
			// The country database is downloaded by provisioning on every box.
			Skip: func(rel string, _ fs.DirEntry, _ bool) bool { return strings.HasSuffix(rel, ".mmdb") },
			Keep: func(bool) []string { return []string{"*.mmdb"} }},
		{Name: "observe", Path: under("observe"), Detail: "error issues, alert rules, retention settings", SQLite: true,
			Skip: func(rel string, d fs.DirEntry, history bool) bool {
				top := strings.SplitN(rel, "/", 2)[0]
				// auth.env is this box's password for its running stores: never another box's.
				if top == "bin" || top == "auth.env" {
					return true
				}
				for _, s := range observeStores {
					if top == s {
						return !history
					}
				}
				return false
			},
			Keep: func(history bool) []string {
				if history {
					return []string{"bin", "auth.env"}
				}
				return append([]string{"bin", "auth.env"}, observeStores...)
			},
			Units: func(history bool) []string {
				if history {
					return []string{"tiffin-metrics.service", "tiffin-logs.service"}
				}
				return nil
			}},
		{Name: "runtime-static", Path: under("runtime/static"), Detail: "static sites of every deploy"},
		{Name: "runtime-git", Path: under("runtime/git"), Detail: "push-to-deploy git repositories"},
		{Name: "runtime-disks", Path: under("runtime/disks"), Detail: "apps' disk folders"},
		{Name: "runtime-disks-trash", Path: under("runtime/disks-trash"), Detail: "deleted apps' disk folders, kept for 7 days"},
		// History.
		{Name: "logs", Path: under("logs"), Detail: "app and edge logs", History: true, Live: true,
			// pgBackRest's own log directory belongs to this box's backups.
			Skip: func(rel string, _ fs.DirEntry, _ bool) bool { return strings.HasPrefix(rel, "pgbackrest") },
			Keep: func(bool) []string { return []string{"pgbackrest"} }},
		{Name: "runtime-deploys", Path: under("runtime/deploys"), Detail: "build logs", History: true, Live: true},
		{Name: "postgres-snapshots", Path: under("backups/snapshots"), Detail: "database snapshots taken before destructive changes", History: true, Live: true},
	}
}

func setByName(name string) (fileSet, bool) {
	for _, s := range sets() {
		if s.Name == name {
			return s, true
		}
	}
	return fileSet{}, false
}

// skip combines the set's own rule with SQLite side files of the databases
// that were snapshotted separately (dbs: relative path → snapshot file).
func (s fileSet) skip(withHistory bool, dbs map[string]string) func(string, fs.DirEntry) bool {
	return func(rel string, d fs.DirEntry) bool {
		if s.Skip != nil && s.Skip(rel, d, withHistory) {
			return true
		}
		if datakit.SQLiteSide(rel) {
			base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(rel, "-wal"), "-shm"), "-journal")
			_, ok := dbs[base]
			return ok
		}
		return false
	}
}

func (s fileSet) keep(withHistory bool) []string {
	if s.Keep == nil {
		return nil
	}
	return s.Keep(withHistory)
}

func (s fileSet) units(withHistory bool) []string {
	if s.Units == nil {
		return nil
	}
	return s.Units(withHistory)
}

// prefix is where the set lives in the archive.
func (s fileSet) prefix() string {
	if s.History {
		return "history/" + s.Name
	}
	return "files/" + s.Name
}
