package storage

import (
	"context"
	"path/filepath"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// BucketDir is where a project's bucket keeps its objects (one file per
// object; content types and ETags in user.* extended attributes). Project
// export reads it; project import fills it once the bucket exists.
func BucketDir(p *platform.Platform, project, bucket string) string {
	return filepath.Join(dataDir(p.DataRoot), S3Name(project, bucket))
}

// InTrash reports whether a bucket of project is in the trash: re-creating
// it (a new project of the same name, say) would bring its files back.
func InTrash(ctx context.Context, p *platform.Platform, project string) bool {
	entries, err := trashEntries(ctx, p)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Project == project && time.Now().Before(e.ExpiresAt) {
			return true
		}
	}
	return false
}
