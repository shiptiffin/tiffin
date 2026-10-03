package analytics

import (
	"context"
	"encoding/json"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
)

// EstimateLoss says how many events go when analytics is removed (all of
// them) or its retention is shortened (those older than the new limit).
func (m *Module) EstimateLoss(ctx context.Context, _ *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	if op.Address != change.KindService+"/analytics" || m.store == nil {
		return nil, nil
	}
	var before time.Time
	switch op.Action {
	case change.Delete:
	case change.Update:
		var s serviceSpec
		if err := json.Unmarshal(op.After, &s); err != nil || s.RetentionDays <= 0 {
			return nil, err
		}
		before = time.Now().AddDate(0, 0, -s.RetentionDays)
	default:
		return nil, nil
	}
	n, b, err := m.store.Footprint(ctx, project, before)
	if err != nil {
		return nil, err
	}
	return &change.Loss{Bytes: b, Counts: []change.LossCount{{N: n, Unit: "event"}}}, nil
}
