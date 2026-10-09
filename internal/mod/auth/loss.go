package auth

import (
	"context"
	"net/http"
	"net/url"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// EstimateLoss says how many people's accounts go when sign-in is removed.
func (*Module) EstimateLoss(ctx context.Context, _ *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	if op.Address != change.KindService+"/auth" || op.Action != change.Delete {
		return nil, nil
	}
	var s Stats
	if err := defaultEngine.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/stats", nil, nil, &s); err != nil {
		return nil, err
	}
	return &change.Loss{Counts: []change.LossCount{{N: int64(s.Users), Unit: "user"}}}, nil
}
