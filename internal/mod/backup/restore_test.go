package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/btahir/tiffin/internal/platform"
)

// liveDB opens a WAL database that keeps its commits in the WAL, like a
// module's store between checkpoints.
func liveDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=journal_mode(wal)&_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE marks (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func mark(t *testing.T, db *sql.DB, v string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO marks VALUES (?)`, v); err != nil {
		t.Fatal(err)
	}
}

func marks(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT v FROM marks ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o700)
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A backup of a live SQLite database holds its WAL commits; restoring it
// swaps it in at the next start, with the old WAL moved away too.
func TestSQLiteFilesBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	if runtime.GOOS != "linux" { // GNU cp's --reflink: a stand-in elsewhere
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "cp"), []byte("#!/bin/sh\nexec /bin/cp -Rp \"$3\" \"$4\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	root := t.TempDir()
	obsPath := filepath.Join(root, "observe", "observe.db")
	anaDir := filepath.Join(root, "analytics")
	_ = os.MkdirAll(filepath.Dir(obsPath), 0o700)
	_ = os.MkdirAll(anaDir, 0o700)
	obs := liveDB(t, obsPath)
	ana := liveDB(t, filepath.Join(anaDir, "analytics.db"))
	mark(t, obs, "backed-up")
	mark(t, ana, "backed-up")
	if err := os.WriteFile(filepath.Join(anaDir, "country.mmdb"), []byte("geo"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The set as `cp` leaves it: main files only, the commits are in the WALs.
	set := filepath.Join(root, "set")
	copyFile(t, obsPath, filepath.Join(set, "files", "observe"))
	copyFile(t, filepath.Join(anaDir, "analytics.db"), filepath.Join(set, "files", "analytics", "analytics.db"))
	copyFile(t, filepath.Join(anaDir, "country.mmdb"), filepath.Join(set, "files", "analytics", "country.mmdb"))
	if got := marks(t, filepath.Join(set, "files", "observe")); len(got) != 0 {
		t.Fatalf("a file copy should miss the WAL here: %v", got)
	}
	if failed, err := snapshotSQLite(ctx, obsPath, filepath.Join(set, "files", "observe")); err != nil || len(failed) > 0 {
		t.Fatal(failed, err)
	}
	if failed, err := snapshotSQLite(ctx, anaDir, filepath.Join(set, "files", "analytics")); err != nil || len(failed) > 0 {
		t.Fatal(failed, err)
	}
	for _, p := range []string{filepath.Join(set, "files", "observe"), filepath.Join(set, "files", "analytics", "analytics.db")} {
		if got := marks(t, p); len(got) != 1 || got[0] != "backed-up" {
			t.Fatalf("snapshot %s: %v", p, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(set, "files", "analytics", "country.mmdb")); string(b) != "geo" {
		t.Fatal("plain files are copied as they are")
	}

	// Later writes, then a restore: staged now, swapped in at the next start.
	mark(t, obs, "later")
	mark(t, ana, "later")
	oldStage, oldAside := restoreStage, restoreAside
	t.Cleanup(func() { restoreStage, restoreAside = oldStage, oldAside })
	restoreStage, restoreAside = filepath.Join(root, "staged"), filepath.Join(root, "aside")
	b := &Backup{ID: "bk_test", Files: map[string]BackupPart{"observe": {Detail: obsPath}, "analytics": {Detail: anaDir}}}
	dataRoot := filepath.Join(root, "data")
	plan := platform.PendingImport{Import: "backup " + b.ID, Aside: restoreAside}
	if err := stageFiles(ctx, set, b, &plan); err != nil {
		t.Fatal(err)
	}
	if err := writePlan(dataRoot, plan); err != nil {
		t.Fatal(err)
	}
	if err := writePlan(dataRoot, plan); err == nil {
		t.Fatal("a second plan must not replace a waiting one")
	}
	if got := marks(t, obsPath); len(got) != 2 {
		t.Fatalf("nothing changes before the restart: %v", got)
	}
	if _, err := os.Stat(obsPath + "-wal"); err != nil {
		t.Fatal("the live WAL should still be there:", err)
	}
	ran, err := platform.ApplyPendingImport(dataRoot, func(string) {})
	if !ran || err != nil {
		t.Fatalf("plan: %v %v", ran, err)
	}
	for _, p := range []string{obsPath, filepath.Join(anaDir, "analytics.db")} {
		if got := marks(t, p); len(got) != 1 || got[0] != "backed-up" {
			t.Fatalf("restored %s: %v", p, got)
		}
	}
	var res platform.PendingResult
	raw, _ := os.ReadFile(platform.PendingResultPath(dataRoot))
	if json.Unmarshal(raw, &res) != nil || !res.OK || res.Import != "backup bk_test" {
		t.Fatalf("result: %s", raw)
	}
	finishFilesRestore(&platform.Platform{DataRoot: dataRoot, Log: slog.New(slog.DiscardHandler)})
	for _, p := range []string{restoreStage, restoreAside, platform.PendingResultPath(dataRoot)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", p)
		}
	}
}
