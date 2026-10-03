package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
)

// EstimateLoss says what deleting a bucket destroys: its files and bytes,
// counted now by walking the bucket. A bucket too big to walk within the
// plan's budget falls back to the last background scan.
func (m *Module) EstimateLoss(ctx context.Context, p *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	if change.Kind(op.Address) != change.KindBucket || op.Action != change.Delete {
		return nil, nil
	}
	s3name := S3Name(project, change.Name(op.Address))
	dir := filepath.Join(dataDir(p.DataRoot), s3name)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // never converged: nothing measured, nothing claimed
		}
		return nil, err
	}
	u, err := walkBucket(ctx, dir)
	if err != nil {
		t := m.tracker()
		if t.at().IsZero() {
			return nil, err
		}
		u = t.bucket(s3name) // the last scan, plus uploads since
	}
	return &change.Loss{Bytes: u.Bytes, Counts: []change.LossCount{{N: u.Objects, Unit: "file"}}}, nil
}

// walkBucket is scanBucket that gives up when ctx ends.
func walkBucket(ctx context.Context, dir string) (Usage, error) {
	var u Usage
	tmp := string(filepath.Separator) + ".sgwtmp" + string(filepath.Separator)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		u.Bytes += info.Size()
		if !strings.Contains(path[len(dir):], tmp) {
			u.Objects++
		}
		return nil
	})
	return u, err
}
