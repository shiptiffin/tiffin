package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

// "Delete all data" of a project's Database (resource emptied/postgres).
// Making the resource snapshots every database of the project (the main one
// and its preview branches), drops them and makes the main database again,
// empty, for the same role and password: apps keep their DATABASE_URL.
// Deleting the resource (Restore, or undoing that change) loads the
// snapshots back, after snapshotting what the database holds by then. The
// snapshots are ordinary ones, kept SnapshotKeep and pruned with the rest.

const nsEmptied = "postgres.emptied" // project → JSON emptiedRecord

// emptiedRecord is one "Delete all data" of a project's database.
type emptiedRecord struct {
	Version int64     `json:"version"` // the emptied/postgres spec's version
	At      time.Time `json:"at"`
	// Snapshots taken first, main database first.
	Snapshots []string `json:"snapshots"`
	// Done: the databases were dropped and the main one made again. A pass
	// that stopped before that finishes the job without new snapshots.
	Done bool `json:"done"`
}

// Seams for tests (a test server has no pg_dump).
var (
	snapshotDB   = takeSnapshot
	loadSnapshot = getSnapshot
	restoreDB    = restoreSnapshotLocked
)

// reconcileEmptied empties the project's databases (spec set) or puts back
// what the last empty took (spec nil). The caller holds mu.
func reconcileEmptied(ctx context.Context, p *platform.Platform, project string, spec json.RawMessage) error {
	var rec emptiedRecord
	raw, have, err := p.DB.KVGet(ctx, nsEmptied, project)
	if err != nil {
		return err
	}
	if have {
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("unreadable record of the deleted data: %w", err)
		}
	}
	pg, on, err := serviceSpec(ctx, p, project)
	if err != nil {
		return err
	}
	if spec == nil {
		if !have {
			return nil
		}
		if on {
			if err := restoreEmptied(ctx, p, project, rec); err != nil {
				return err
			}
		}
		// Without the service the project is being deleted: the snapshots
		// stay for SnapshotKeep like any other, the record goes.
		return p.DB.KVDelete(ctx, nsEmptied, project)
	}
	var s change.EmptiedSpec
	if err := json.Unmarshal(spec, &s); err != nil {
		return fmt.Errorf("emptied spec: %w", err)
	}
	if have && rec.Version == s.Version && rec.Done {
		return nil
	}
	if !on {
		return errors.New("the project has no database to empty")
	}
	if !have || rec.Version != s.Version {
		rec = emptiedRecord{Version: s.Version, At: time.Now().UTC()}
	}
	return emptyDatabases(ctx, p, project, pg, rec)
}

// serviceSpec is the project's postgres service spec, and whether it has one.
func serviceSpec(ctx context.Context, p *platform.Platform, project string) (manifest.Postgres, bool, error) {
	var s manifest.Postgres
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return s, false, err
	}
	r, ok := res[change.KindService+"/postgres"]
	if !ok {
		return s, false, nil
	}
	return s, true, json.Unmarshal(r.Spec, &s)
}

func emptyDatabases(ctx context.Context, p *platform.Platform, project string, pg manifest.Postgres, rec emptiedRecord) error {
	admin, err := dbAdmin(ctx, "postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	dbs, err := projectDatabases(ctx, admin, project)
	if err != nil {
		return err
	}
	if len(rec.Snapshots) == 0 {
		for _, d := range dbs {
			snap, err := snapshotDB(ctx, project, d.Name, d.Branch, "Delete all data")
			if err != nil {
				return fmt.Errorf("snapshot %s before deleting its data (nothing was deleted): %w", d.Name, err)
			}
			rec.Snapshots = append(rec.Snapshots, snap.ID)
		}
		// Recorded before anything is dropped: from here on the data can
		// always be put back.
		if err := putEmptied(ctx, p, project, rec); err != nil {
			return err
		}
	}
	for _, d := range dbs {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(d.Name))); err != nil {
			return fmt.Errorf("drop %s: %w", d.Name, err)
		}
	}
	forgetPooled(ctx, Database(project))
	// The main database again, empty, with the extensions the project asks for.
	if err := ensure(ctx, p, project, pg); err != nil {
		return err
	}
	rec.Done = true
	if err := putEmptied(ctx, p, project, rec); err != nil {
		return err
	}
	p.Log.Info("postgres: deleted all data", "project", project, "snapshots", len(rec.Snapshots))
	// Auth keeps its tables in this database: let it make them again.
	p.ReconcileProject(project)
	return nil
}

// restoreEmptied loads the snapshots an empty took back, each into its own
// database (what is there now is snapshotted first). A snapshot past
// SnapshotKeep is gone: there is nothing to put back then.
func restoreEmptied(ctx context.Context, p *platform.Platform, project string, rec emptiedRecord) error {
	for _, id := range rec.Snapshots {
		s, err := loadSnapshot(project, id)
		if errors.Is(err, os.ErrNotExist) {
			p.Log.Warn("postgres: the snapshot of the deleted data is gone; nothing to restore", "project", project, "snapshot", id)
			continue
		}
		if err != nil {
			return err
		}
		if _, err := restoreDB(ctx, p, s); err != nil {
			return fmt.Errorf("restore %s from %s: %w", s.Database, s.ID, err)
		}
	}
	p.Log.Info("postgres: restored the deleted data", "project", project, "snapshots", len(rec.Snapshots))
	p.ReconcileProject(project)
	return nil
}

func putEmptied(ctx context.Context, p *platform.Platform, project string, rec emptiedRecord) error {
	raw, _ := json.Marshal(rec)
	return p.DB.KVPut(ctx, nsEmptied, project, raw)
}
