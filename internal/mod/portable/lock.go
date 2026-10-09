package portable

import (
	"context"
	"errors"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/backup"
)

// exclusive takes the lock backups, restores, exports and imports share,
// waiting up to wait for one that is running (a scheduled backup takes
// seconds to minutes) instead of failing at once.
func exclusive(ctx context.Context, wait time.Duration, waiting func()) (func(), error) {
	deadline := time.Now().Add(wait)
	told := false
	for {
		release, err := backup.Exclusive()
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, backup.ErrBusy) || time.Now().After(deadline) {
			return nil, err
		}
		if !told && waiting != nil {
			waiting()
			told = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
