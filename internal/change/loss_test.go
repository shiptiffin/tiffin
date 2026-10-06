package change

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLossSummary(t *testing.T) {
	for _, c := range []struct {
		l    Loss
		want string
	}{
		{Loss{Bytes: 43 << 20, Counts: []LossCount{{N: 18204, Unit: "row"}, {N: 12, Unit: "table"}}}, "18,204 rows in 12 tables · 43 MB"},
		{Loss{Bytes: 1536, Counts: []LossCount{{N: 1, Unit: "file"}}}, "1 file · 1.5 KB"},
		{Loss{Counts: []LossCount{{N: 2_400_000, Unit: "row", Approx: true}, {N: 1, Unit: "table"}}}, "about 2,400,000 rows in 1 table"},
		{Loss{Counts: []LossCount{{N: 0, Unit: "job"}}}, "nothing: it is empty"},
		{Loss{Bytes: 3 << 30, Counts: []LossCount{{N: 2, Unit: "database"}}}, "2 databases · 3 GB"},
	} {
		if got := c.l.Summarize().Summary; got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	if Thousands(-1234567) != "-1,234,567" || Thousands(999) != "999" {
		t.Error("thousands")
	}
	if Bytes(150<<20) != "150 MB" || Bytes(512) != "512 B" {
		t.Errorf("bytes: %s %s", Bytes(150<<20), Bytes(512))
	}
}

func TestAttachLosses(t *testing.T) {
	ops := []Op{
		{Action: Delete, Address: "service/postgres", Risk: TierIrreversible},
		{Action: Delete, Address: "bucket/slow", Risk: TierIrreversible},
		{Action: Delete, Address: "bucket/broken", Risk: TierIrreversible},
		{Action: Delete, Address: "queue/unknown", Risk: TierIrreversible},
		{Action: Delete, Address: "app/web", Risk: TierReversible},
	}
	p := newPlan("shop", 3, ops, "")
	hash := p.Hash
	start := time.Now()
	AttachLosses(context.Background(), p, func(ctx context.Context, project string, op Op) (*Loss, error) {
		switch op.Address {
		case "service/postgres":
			return &Loss{Bytes: 9 << 20, Counts: []LossCount{{N: 1180, Unit: "row"}, {N: 3, Unit: "table"}}}, nil
		case "bucket/slow":
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			return &Loss{Counts: []LossCount{{N: 1, Unit: "file"}}}, nil
		case "bucket/broken":
			return nil, errors.New("disk on fire")
		}
		return nil, nil
	})
	if d := time.Since(start); d > LossBudget+100*time.Millisecond {
		t.Errorf("took %v; the budget is %v", d, LossBudget)
	}
	if l := p.Ops[0].Loss; l == nil || l.Summary != "1,180 rows in 3 tables · 9 MB" {
		t.Errorf("postgres: %+v", l)
	}
	for _, i := range []int{1, 2, 3, 4} {
		if p.Ops[i].Loss != nil {
			t.Errorf("%s: %+v; want no estimate", p.Ops[i].Address, p.Ops[i].Loss)
		}
	}
	if p.Hash != hash || hashPlan(p) != hash {
		t.Error("a loss estimate changed the plan hash")
	}
}

func TestEngineAttachesLosses(t *testing.T) {
	s := NewMemStore()
	e := NewEngine(s)
	ctx := context.Background()
	desired := map[string]Resource{KindProject: {Address: KindProject, Spec: []byte(`{}`)}, "bucket/uploads": {Address: "bucket/uploads", Spec: []byte(`{}`)}}
	p, _ := e.Plan(ctx, "shop", desired)
	if _, err := e.Apply(ctx, ApplyRequest{Plan: p, Confirm: p.Hash}); err != nil {
		t.Fatal(err)
	}
	e.Estimate = func(ctx context.Context, project string, op Op) (*Loss, error) {
		return &Loss{Bytes: 2048, Counts: []LossCount{{N: 2, Unit: "file"}}}, nil
	}
	delete(desired, "bucket/uploads")
	p, err := e.Plan(ctx, "shop", desired)
	if err != nil || len(p.Ops) != 1 || p.Ops[0].Loss == nil || p.Ops[0].Loss.Summary != "2 files · 2 KB" {
		t.Fatalf("plan: %+v %v", p, err)
	}
	c, err := e.Apply(ctx, ApplyRequest{Plan: p, Confirm: p.Hash})
	if err != nil || c.Plan.Ops[0].Loss == nil {
		t.Fatalf("the change keeps what was lost: %+v %v", c, err)
	}
}

func TestNewProjectNames(t *testing.T) {
	e := NewEngine(NewMemStore())
	desired := map[string]Resource{KindProject: {Address: KindProject, Spec: []byte(`{}`)}}
	for _, name := range []string{"shop--read", "shop-", "Shop", "a-very-long-project-name-that-goes-past-forty"} {
		if _, err := e.Plan(context.Background(), name, desired); err == nil {
			t.Errorf("%q planned as a new project", name)
		}
	}
	if _, err := e.Plan(context.Background(), "my-shop-2", desired); err != nil {
		t.Fatal(err)
	}
}
