package hetzner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

func TestPlanResizePricesAndRefuses(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	p := newProvider(t, f, nil)
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	before := len(f.Mutations)

	r, err := p.PlanResize(ctx, "cax21", 80)
	if err != nil {
		t.Fatal(err)
	}
	if r.From.Name != "cax11" || r.To == nil || r.To.Name != "cax21" || r.To.MemoryGB != 8 || r.To.Cores != 4 || !r.Downtime {
		t.Fatalf("plan %+v", r)
	}
	if r.VolumeFromGB != 40 || r.VolumeToGB != 80 || r.ServerDiskGB != 40 {
		t.Fatalf("volume/disk %+v", r)
	}
	// before: 4.49 + 0.61 + 40 × 0.0572 (2.29) = 7.39; after: 7.99 + 0.61 + 80 × 0.0572 (4.58) = 13.18
	if r.MonthlyNetBefore != 7.39 || r.MonthlyNetAfter != 13.18 || r.MonthlyGrossAfter <= r.MonthlyNetAfter {
		t.Fatalf("prices %v → %v (gross %v)", r.MonthlyNetBefore, r.MonthlyNetAfter, r.MonthlyGrossAfter)
	}
	steps := strings.Join(r.Steps, "\n")
	for _, want := range []string{"grow the data volume from 40 to 80 GB", "stop Tiffin", "its own 40 GB disk stays", "about 2 minutes"} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps lack %q:\n%s", want, steps)
		}
	}

	// Growing the volume alone has no downtime.
	if r, err := p.PlanResize(ctx, "", 100); err != nil || r.To != nil || r.Downtime || r.VolumeToGB != 100 {
		t.Fatalf("volume only: %+v %v", r, err)
	}
	if r, err := p.PlanResize(ctx, "cax11", 40); err != nil || !r.Empty() {
		t.Fatalf("same size: %+v %v", r, err)
	}

	for _, tc := range []struct {
		typ  string
		gb   int
		want string
	}{
		{"cx33", 0, "cax11 is ARM and cx33 is x86: Hetzner cannot move a server between ARM and x86"},
		{"cpx31", 0, "ARM types this box can change to: cax21 (4 vCPU ARM, 8 GB RAM, 80 GB disk) 7.99 EUR/month"},
		{"cax99", 0, "unknown Hetzner server type"},
		{"", 20, "volumes cannot shrink"},
		{"", 20000, "at most 10240 GB"},
	} {
		_, err := p.PlanResize(ctx, tc.typ, tc.gb)
		var ref *Refusal
		if !errors.As(err, &ref) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s/%d: want a refusal with %q, got %v", tc.typ, tc.gb, tc.want, err)
		}
	}
	if got := f.Mutations[before:]; len(got) != 0 {
		t.Fatalf("planning changed %v", got)
	}

	// cax41 is offered in fsn1 only; the box is in fsn1, so it is fine.
	if _, err := p.PlanResize(ctx, "cax41", 0); err != nil {
		t.Fatal(err)
	}
}

func TestPlanResizeRefusesASmallerDisk(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	p := newProvider(t, f, func(c *Config) { c.ServerType = "cax31" })
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	_, err := p.PlanResize(ctx, "cax21", 0)
	if err == nil || !strings.Contains(err.Error(), "this server's disk is 160 GB and cax21 has only 80 GB") {
		t.Fatalf("want a disk refusal, got %v", err)
	}
	// Bigger is fine, and the upgrade list starts above the current type.
	if _, err := p.PlanResize(ctx, "cax41", 0); err != nil {
		t.Fatal(err)
	}
	m, err := p.Machine(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if m.ServerType.Name != "cax31" || len(m.Upgrades) != 1 || m.Upgrades[0].Name != "cax41" || m.VolumeGB != 40 || m.Currency != "EUR" || m.VolumeGBNet != 0.0572 {
		t.Fatalf("machine %+v", m)
	}
}

func TestChangeTypeAndGrowVolume(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	p := newProvider(t, f, nil)
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	m, err := p.Machine(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range m.Upgrades {
		names = append(names, o.Name)
	}
	if !slices.Equal(names, []string{"cax21", "cax31", "cax41"}) {
		t.Fatalf("upgrades %v", names)
	}

	// An adopted server is protected; protection does not block a type change.
	sid := f.ServerByName("shop").ID
	f.Protect(sid)
	before := len(f.Mutations)
	var said []string
	if err := p.ChangeType(ctx, "cax21", func(s string) { said = append(said, s) }); err != nil {
		t.Fatal(err)
	}
	var acts []string
	for _, mu := range f.Mutations[before:] {
		acts = append(acts, mu[strings.LastIndex(mu, "/")+1:])
	}
	if !slices.Equal(acts, []string{"shutdown", "change_type", "poweron"}) {
		t.Fatalf("want shutdown, change_type, poweron; got %v", f.Mutations[before:])
	}
	if !slices.Equal(f.ChangeTypes, []string{"cax21 upgrade_disk=false"}) {
		t.Fatalf("the disk must stay (downgrades stay possible): %v", f.ChangeTypes)
	}
	srv := f.ServerByName("shop")
	if srv.ServerType.Name != "cax21" || srv.Status != "running" || srv.PrimaryDiskSize != 40 {
		t.Fatalf("server after: %s %s disk %d", srv.ServerType.Name, srv.Status, srv.PrimaryDiskSize)
	}
	// Back down to cax11: possible because the disk stayed 40 GB.
	if _, err := p.PlanResize(ctx, "cax11", 0); err != nil {
		t.Fatalf("downgrade: %v", err)
	}

	// Hetzner's refusals are passed on plainly, and the server is started again.
	err = p.ChangeType(ctx, "cx33", quiet)
	if err == nil || !strings.Contains(err.Error(), "Hetzner said server type has a different architecture") || f.ServerByName("shop").Status != "running" {
		t.Fatalf("want Hetzner's refusal and a running server, got %v", err)
	}

	if err := p.GrowVolume(ctx, 120, quiet); err != nil {
		t.Fatal(err)
	}
	if f.Volume().Size != 120 {
		t.Fatalf("volume %d GB", f.Volume().Size)
	}
	before = len(f.Mutations)
	if err := p.GrowVolume(ctx, 100, quiet); err != nil || len(f.Mutations) != before {
		t.Fatalf("a smaller size never shrinks: %v %v", err, f.Mutations[before:])
	}
}

// A size Hetzner has sold out where the box is shows in the list, marked,
// but is never offered for an actual change.
func TestOptionsMarkSoldOutSizes(t *testing.T) {
	nbg := &hcloud.Location{Name: "nbg1"}
	st := func(name string, cores int, mem float32, disk int, free bool) *hcloud.ServerType {
		return &hcloud.ServerType{Name: name, Architecture: hcloud.ArchitectureX86, Cores: cores, Memory: mem, Disk: disk,
			Locations: []hcloud.ServerTypeLocation{{Location: nbg, Available: free}}}
	}
	cur, sold, free := st("cx23", 2, 4, 40, true), st("cx33", 4, 8, 80, false), st("cpx32", 4, 8, 160, true)
	price := func(s *hcloud.ServerType, net string) hcloud.ServerTypePricing {
		return hcloud.ServerTypePricing{ServerType: s, Pricings: []hcloud.ServerTypeLocationPricing{{Location: nbg, Monthly: hcloud.Price{Net: net, Gross: net}}}}
	}
	pr := hcloud.Pricing{ServerTypes: []hcloud.ServerTypePricing{price(cur, "6.49"), price(sold, "9.99"), price(free, "41.99")}}
	srv := &hcloud.Server{ServerType: cur, Location: nbg}
	all := []*hcloud.ServerType{cur, sold, free}
	shown := options(all, srv, pr, true, true)
	if len(shown) != 2 || shown[0].Name != "cx33" || !shown[0].SoldOut || shown[1].Name != "cpx32" || shown[1].SoldOut {
		t.Fatalf("shown: %+v", shown)
	}
	if orderable := options(all, srv, pr, true, false); len(orderable) != 1 || orderable[0].Name != "cpx32" {
		t.Fatalf("orderable: %+v", orderable)
	}
}
