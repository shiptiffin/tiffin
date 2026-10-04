package valkey

import (
	"context"

	"github.com/btahir/tiffin/internal/platform"
)

// ProjectUsage reports the project's keys and the memory they take, for
// the project usage API. Valkey keeps its data in memory (with snapshots on
// the data disk), so the bytes count as the project's KV data.
func (*Module) ProjectUsage(ctx context.Context, p *platform.Platform, project string) (*platform.ServiceUsage, error) {
	if ok, err := HasService(ctx, p, project); err != nil || !ok {
		return nil, err
	}
	st, err := GetStats(ctx, p, project)
	if err != nil {
		return nil, err
	}
	return &platform.ServiceUsage{Service: "valkey", Disk: "kv", Bytes: st.MemoryBytes, Counts: map[string]int64{"keys": st.Keys}}, nil
}
