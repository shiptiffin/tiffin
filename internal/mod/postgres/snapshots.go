package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
)

// Snapshot is a logical copy (pg_dump, custom format) of one database,
// taken before something destructive: deleting the service, dropping an
// extension, an SQL write, a snapshot restore. Kept for SnapshotKeep.
type PGSnapshot struct {
	ID        string    `json:"id" doc:"Snapshot ID (snap_...)"`
	Project   string    `json:"project"`
	Database  string    `json:"database"`
	Branch    string    `json:"branch,omitempty" doc:"Set when the snapshot is of a preview branch"`
	Reason    string    `json:"reason" doc:"Why it was taken, e.g. \"service deleted\""`
	At        time.Time `json:"at"`
	SizeBytes int64     `json:"sizeBytes"`
	ExpiresAt time.Time `json:"expiresAt"`
	Path      string    `json:"-"`
}

func snapDir(project string) string { return filepath.Join(SnapshotDir, project) }

// takeSnapshot dumps db with pg_dump (custom format, zstd) and records why.
func takeSnapshot(ctx context.Context, project, db, branch, reason string) (*PGSnapshot, error) {
	dir := snapDir(project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &PGSnapshot{ID: ids.New("snap"), Project: project, Database: db, Branch: branch, Reason: reason, At: time.Now().UTC()}
	s.Path = filepath.Join(dir, s.ID+".dump")
	tmp := s.Path + ".part"
	if _, err := datakit.Run(ctx, BinDir+"/pg_dump", "-h", SocketDir, "-p", fmt.Sprint(Port), "-U", "postgres",
		"--format=custom", "--compress=zstd:3", "--file="+tmp, db); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(s.Path); err == nil {
		s.SizeBytes = fi.Size()
	}
	s.ExpiresAt = s.At.Add(SnapshotKeep)
	raw, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, s.ID+".json"), raw, 0o600); err != nil {
		return nil, err
	}
	return s, nil
}

func getSnapshot(project, id string) (*PGSnapshot, error) {
	raw, err := os.ReadFile(filepath.Join(snapDir(project), filepath.Base(id)+".json"))
	if err != nil {
		return nil, err
	}
	var s PGSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	s.Path = filepath.Join(snapDir(project), s.ID+".dump")
	if _, err := os.Stat(s.Path); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSnapshots returns a project's snapshots, newest first.
func ListSnapshots(project string) ([]PGSnapshot, error) {
	entries, err := os.ReadDir(snapDir(project))
	if os.IsNotExist(err) {
		return []PGSnapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []PGSnapshot{}
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if s, err := getSnapshot(project, id); err == nil {
				out = append(out, *s)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// pgRestore loads a snapshot into an existing, empty database of project,
// logged in as the project's own role: a snapshot holds the project's
// functions, CHECK constraints and defaults, which run during the restore,
// so they must never run as the superuser (a SET ROLE on a superuser session
// is no boundary: the function can RESET it). Objects come out owned by the
// role (--no-owner). The extensions the snapshot uses are made first, as the
// superuser, as its config may ask for any, and their entries skipped.
func pgRestore(ctx context.Context, p *platform.Platform, project, path, db string) error {
	pw, ok, err := datakit.GetSecret(ctx, p, nsPassword, project)
	if err != nil {
		return err
	}
	if !ok {
		return errNoService(project)
	}
	toc, err := datakit.Run(ctx, BinDir+"/pg_restore", "--list", path)
	if err != nil {
		return err
	}
	list, exts := restoreList(toc)
	if len(exts) > 0 {
		c, err := dbAdmin(ctx, db)
		if err != nil {
			return err
		}
		for _, e := range exts {
			if err := CreateExtension(ctx, c, e); err != nil {
				c.Close(ctx)
				return fmt.Errorf("extension %s: %w", e, err)
			}
		}
		c.Close(ctx)
	}
	lf, err := os.CreateTemp(SnapshotDir, "restore-*.list")
	if err != nil {
		return err
	}
	defer os.Remove(lf.Name())
	if _, err := lf.WriteString(list); err != nil {
		lf.Close()
		return err
	}
	if err := lf.Close(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, BinDir+"/pg_restore", restoreArgs(Role(project), db, lf.Name(), path)...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C.UTF-8", "PGAPPNAME=tiffin-restore", "PGPASSWORD=" + pw,
		// The role's own limits are for its app's queries, not for loading
		// a whole database in one transaction.
		"PGOPTIONS=-c statement_timeout=0 -c lock_timeout=0 -c idle_in_transaction_session_timeout=0 -c default_transaction_read_only=off"}
	cmd.WaitDelay = 5 * time.Second
	var out datakit.TailBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, out.String())
	}
	return nil
}

func restoreArgs(role, db, list, path string) []string {
	return []string{"-h", SocketDir, "-p", fmt.Sprint(Port), "-U", role, "--no-password",
		"--no-owner", "--exit-on-error", "--single-transaction", "--use-list=" + list, "--dbname=" + db, path}
}

// restoreList comments out the extension entries (and their comments) of a
// pg_restore --list table of contents and returns the extensions' names.
// Entries look like "3; 3079 16385 EXTENSION - vector" and
// "4012; 0 0 COMMENT - EXTENSION vector" (the name quoted when it must be).
func restoreList(toc string) (string, []string) {
	var b strings.Builder
	var exts []string
	for line := range strings.Lines(toc) {
		f := strings.Fields(line)
		if len(f) >= 6 && strings.HasSuffix(f[0], ";") && f[4] == "-" {
			switch {
			case f[3] == "EXTENSION":
				exts = append(exts, f[5])
				b.WriteString(";")
			case f[3] == "COMMENT" && f[5] == "EXTENSION":
				b.WriteString(";")
			}
		}
		b.WriteString(line)
	}
	return b.String(), exts
}

// restoreSnapshot replaces the snapshot's database with the snapshot's
// contents, after snapshotting what is there now.
func restoreSnapshot(ctx context.Context, p *platform.Platform, s *PGSnapshot) (*PGSnapshot, error) {
	mu.Lock()
	defer mu.Unlock()
	return restoreSnapshotLocked(ctx, p, s)
}

// restoreSnapshotLocked is restoreSnapshot for a caller that holds mu.
func restoreSnapshotLocked(ctx context.Context, p *platform.Platform, s *PGSnapshot) (*PGSnapshot, error) {
	admin, err := dbAdmin(ctx, "postgres")
	if err != nil {
		return nil, err
	}
	defer admin.Close(ctx)
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, s.Database).Scan(&exists); err != nil {
		return nil, err
	}
	var before *PGSnapshot
	if exists {
		if before, err = takeSnapshot(ctx, s.Project, s.Database, s.Branch, "before restoring "+s.ID); err != nil {
			return nil, fmt.Errorf("snapshot the current data first (nothing was changed): %w", err)
		}
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE %s WITH (FORCE)`, quoteIdent(s.Database))); err != nil {
			return nil, err
		}
	}
	if err := createDatabase(ctx, admin, s.Database, Role(s.Project), ""); err != nil {
		return nil, err
	}
	meta := dbMeta{Tiffin: "main", Project: s.Project, CreatedAt: time.Now().UTC()}
	if s.Branch != "" {
		meta = dbMeta{Tiffin: "branch", Project: s.Project, Branch: s.Branch, From: s.ID, CreatedAt: time.Now().UTC()}
	}
	if err := setMeta(ctx, admin, s.Database, meta); err != nil {
		return nil, err
	}
	poolsChanged()
	return before, pgRestore(ctx, p, s.Project, s.Path, s.Database)
}

// pruneSnapshots deletes snapshots older than SnapshotKeep.
func pruneSnapshots(log func(msg string, args ...any)) {
	projects, _ := os.ReadDir(SnapshotDir)
	for _, pd := range projects {
		if !pd.IsDir() {
			continue
		}
		list, _ := ListSnapshots(pd.Name())
		for _, s := range list {
			if time.Since(s.At) > SnapshotKeep {
				os.Remove(s.Path)
				os.Remove(filepath.Join(snapDir(pd.Name()), s.ID+".json"))
				log("postgres: deleted expired snapshot", "project", pd.Name(), "snapshot", s.ID)
			}
		}
		// Leftovers of interrupted dumps.
		entries, _ := os.ReadDir(snapDir(pd.Name()))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".part") {
				if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
					os.Remove(filepath.Join(snapDir(pd.Name()), e.Name()))
				}
			}
		}
	}
}
