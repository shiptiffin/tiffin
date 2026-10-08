package valkey

import (
	"context"

	"github.com/btahir/tiffin/internal/change"
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
	return &platform.ServiceUsage{Service: "valkey", Disk: "kv", Bytes: st.MemoryBytes,
		Counts: map[string]int64{"keys": st.Keys, "maxMemoryMB": int64(st.MaxMemoryMB)}}, nil
}

// EstimateLoss says what Delete all data of the project's KV deletes (and
// what its restore replaces): the keys under its prefix and their memory.
func (*Module) EstimateLoss(ctx context.Context, p *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	if op.Address != change.EmptyAddress("valkey") {
		return nil, nil
	}
	c, err := Admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	keys, bytes, approx, err := prefixUsage(ctx, c, Prefix(project))
	if err != nil {
		return nil, err
	}
	return &change.Loss{Bytes: bytes, Counts: []change.LossCount{{N: keys, Unit: "key", Approx: approx}}}, nil
}
