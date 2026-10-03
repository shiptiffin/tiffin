package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// pgRestore loads a snapshot into an existing (empty) database.
func pgRestore(ctx context.Context, path, db string) error {
	_, err := datakit.Run(ctx, BinDir+"/pg_restore", "-h", SocketDir, "-p", fmt.Sprint(Port), "-U", "postgres",
		"--exit-on-error", "--single-transaction", "--dbname="+db, path)
	return err
}

// restoreSnapshot replaces the snapshot's database with the snapshot's
// contents, after snapshotting what is there now.
func restoreSnapshot(ctx context.Context, p *platform.Platform, s *PGSnapshot) (*PGSnapshot, error) {
	mu.Lock()
	defer mu.Unlock()
	admin, err := Admin(ctx, "postgres")
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
	role := Role(s.Project)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, quoteIdent(s.Database), quoteIdent(role))); err != nil {
		return nil, err
	}
	meta := dbMeta{Tiffin: "main", Project: s.Project, CreatedAt: time.Now().UTC()}
	if s.Branch != "" {
		meta = dbMeta{Tiffin: "branch", Project: s.Project, Branch: s.Branch, From: s.ID, CreatedAt: time.Now().UTC()}
	}
	if err := setMeta(ctx, admin, s.Database, meta); err != nil {
		return nil, err
	}
	if err := pgRestore(ctx, s.Path, s.Database); err != nil {
		return before, err
	}
	_, err = admin.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON DATABASE %[1]s FROM PUBLIC; GRANT ALL ON DATABASE %[1]s TO %[2]s`, quoteIdent(s.Database), quoteIdent(role)))
	return before, err
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
