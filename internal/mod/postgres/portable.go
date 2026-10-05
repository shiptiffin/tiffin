package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// Prepare makes a project's role and its empty main database (Tiffin's
// helper schema, the extensions) before the project's postgres service is
// applied, so a project import can load its data first. The reconcile that
// follows finds both in place.
func Prepare(ctx context.Context, p *platform.Platform, project string, extensions []string) error {
	mu.Lock()
	defer mu.Unlock()
	return ensure(ctx, p, project, extensions)
}

// Unprepare drops what Prepare made: an import that failed before its
// project existed.
func Unprepare(ctx context.Context, p *platform.Platform, project string) error {
	mu.Lock()
	defer mu.Unlock()
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(Database(project)))); err != nil {
		return err
	}
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, Role(project)).Scan(&exists); err != nil {
		return err
	}
	if exists {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %[1]s; DROP ROLE %[1]s`, quoteIdent(Role(project)))); err != nil {
			return err
		}
	}
	_ = p.DB.KVDelete(ctx, nsExtensions, project)
	return p.DB.KVDelete(ctx, nsPassword, project)
}

// RecentlyDeleted reports whether a project of this name had its database
// deleted within the undo window: creating it again would bring that data
// back.
func RecentlyDeleted(ctx context.Context, p *platform.Platform, project string) bool {
	raw, ok, err := p.DB.KVGet(ctx, nsDeleted, project)
	if err != nil || !ok {
		return false
	}
	var rec deletedRecord
	return json.Unmarshal(raw, &rec) != nil || time.Since(rec.At) < SnapshotKeep
}
