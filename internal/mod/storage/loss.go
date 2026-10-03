package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
)

// EstimateLoss says what deleting a bucket destroys: its files and bytes,
// from the last usage scan when there is one, else by walking the bucket.
func (m *Module) EstimateLoss(ctx context.Context, p *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	if change.Kind(op.Address) != change.KindBucket || op.Action != change.Delete {
		return nil, nil
	}
	s3name := S3Name(project, change.Name(op.Address))
	var u Usage
	if t := m.tracker(); !t.at().IsZero() {
		u = t.bucket(s3name)
	} else {
		dir := filepath.Join(dataDir(p.DataRoot), s3name)
		if _, err := os.Stat(dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil // never converged: nothing measured, nothing claimed
			}
			return nil, err
		}
		u = scanBucket(dir)
	}
	return &change.Loss{Bytes: u.Bytes, Counts: []change.LossCount{{N: u.Objects, Unit: "file"}}}, nil
}
