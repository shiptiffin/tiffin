package hetzner

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/btahir/tiffin/internal/provider/remote"
)

func newProvider(t *testing.T, f *hetznertest.Fake, mod func(*Config)) *Provider {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{Token: hetznertest.Token, Endpoint: f.URL, Name: "shop", SSHFrom: []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")},
		KeyPath: filepath.Join(dir, "id_ed25519"), KnownHosts: filepath.Join(dir, "known_hosts"), PollInterval: 10 * time.Millisecond}
	if mod != nil {
		mod(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func quiet(string) {}

func TestPlanPricesWithoutCreating(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	pl, err := newProvider(t, f, nil).Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pl.ServerType != "cax11" || pl.Location != "fsn1" || pl.Arch != "arm64" || pl.VolumeGB != 40 || pl.Currency != "EUR" {
		t.Fatalf("plan: %+v", pl)
	}
	// server 4.49 + IPv4 0.61 + 40 GB × 0.0572 (2.288 → 2.29)
	if len(pl.Costs) != 3 || pl.MonthlyNet != 7.39 {
		t.Fatalf("costs: %+v total %v", pl.Costs, pl.MonthlyNet)
	}
	if pl.MonthlyGross <= pl.MonthlyNet {
		t.Fatalf("gross %v must include VAT over net %v", pl.MonthlyGross, pl.MonthlyNet)
	}
	if len(pl.Create) != 4 || len(pl.Reuse) != 0 || !strings.Contains(strings.Join(pl.Create, "\n"), "SSH from 198.51.100.7/32") {
		t.Fatalf("create: %q reuse: %q", pl.Create, pl.Reuse)
	}
	if len(f.Mutations) != 0 {
		t.Fatalf("a dry run must not change anything: %v", f.Mutations)
	}

	x86, err := newProvider(t, f, func(c *Config) { c.ServerType, c.Location = "cx23", "ash" }).Plan(context.Background())
	if err != nil || x86.Arch != "amd64" {
		t.Fatalf("cx23 in ash: %+v %v", x86, err)
	}
}

func TestResolveRefusesWhatCannotBeOrdered(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	for _, tc := range []struct {
		mod  func(*Config)
		want string
	}{
		{func(c *Config) { c.Location = "ash" }, "cax11 (ARM) cannot be ordered in ash: it is available in fsn1, hel1, nbg1"},
		{func(c *Config) { c.ServerType = "cx99" }, "unknown Hetzner server type"},
		{func(c *Config) { c.Location = "mars1" }, "unknown Hetzner location \"mars1\"; choose one of: fsn1 (Falkenstein)"},
		{func(c *Config) { c.Token = "wrong" }, "rejected the API token"},
	} {
		_, err := newProvider(t, f, tc.mod).Plan(context.Background())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, err)
		}
	}
	if _, err := New(Config{Name: "shop"}); err == nil || !strings.Contains(err.Error(), "HCLOUD_TOKEN") {
		t.Errorf("missing token: %v", err)
	}
	if err := ValidName("Bad_Name"); err == nil {
		t.Error("Bad_Name must be refused")
	}
}

func TestEnsureCreatesOnceAndConverges(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	p := newProvider(t, f, nil)
	tg, err := p.Ensure(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if s, v, fw, k := f.Count(); s != 1 || v != 1 || fw != 1 || k != 1 {
		t.Fatalf("want one of each, got servers %d volumes %d firewalls %d keys %d", s, v, fw, k)
	}
	srv := f.ServerByName("shop")
	if srv.Labels["tiffin"] != "box" || srv.Labels["tiffin-box"] != "shop" {
		t.Fatalf("server labels: %v", srv.Labels)
	}
	if tg.User != "root" || tg.Host != srv.PublicNet.IPv4.IP || tg.Identity != p.cfg.KeyPath {
		t.Fatalf("target: %+v", tg)
	}
	if fi, err := os.Stat(p.cfg.KeyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key must be 0600: %v %v", fi.Mode(), err)
	}
	vol := f.Volume()
	if vol.Server == nil || *vol.Server != srv.ID || vol.Size != 40 || vol.Labels["tiffin-box"] != "shop" {
		t.Fatalf("volume: %+v", vol)
	}
	if !strings.HasPrefix(p.VolumeDevice(), "/dev/disk/by-id/scsi-0HC_Volume_") {
		t.Fatalf("device %q", p.VolumeDevice())
	}
	fw := f.Firewall()
	var ssh []string
	for _, r := range fw.Rules {
		if r.Port != nil && *r.Port == "22" {
			ssh = r.SourceIPs
		}
	}
	if !slices.Equal(ssh, []string{"198.51.100.7/32"}) || len(fw.Rules) != 4 || len(fw.AppliedTo) != 1 {
		t.Fatalf("firewall: ssh from %v, %d rules, applied to %v", ssh, len(fw.Rules), fw.AppliedTo)
	}
	v4, v6 := PublicIPs(p.Server)
	if v4 == "" || !strings.HasSuffix(v6, "::1") {
		t.Fatalf("ips %q %q", v4, v6)
	}

	// Again: nothing new is created.
	before := len(f.Mutations)
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if got := f.Mutations[before:]; len(got) != 0 {
		t.Fatalf("a second up must not change anything: %v", got)
	}

	// From another network: only the firewall's SSH rule changes.
	moved := newProvider(t, f, func(c *Config) {
		c.KeyPath, c.KnownHosts = p.cfg.KeyPath, p.cfg.KnownHosts
		c.SSHFrom = []netip.Prefix{netip.MustParsePrefix("192.0.2.9/32")}
	})
	before = len(f.Mutations)
	if _, err := moved.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if got := f.Mutations[before:]; len(got) != 1 || !strings.HasSuffix(got[0], "/actions/set_rules") {
		t.Fatalf("want only set_rules, got %v", got)
	}

	// A different computer (no key file) cannot take over the server.
	other := newProvider(t, f, nil)
	if _, err := other.Ensure(ctx, quiet); err == nil || !strings.Contains(err.Error(), "does not have its SSH key") {
		t.Fatalf("want a missing-key error, got %v", err)
	}
}

func TestDestroyKeepsDataUnlessAsked(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	p := newProvider(t, f, nil)
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	volID := f.Volume().ID
	rep, err := p.DestroyAll(ctx, false, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if s, v, fw, k := f.Count(); s != 0 || v != 1 || fw != 0 || k != 0 {
		t.Fatalf("after down: servers %d volumes %d firewalls %d keys %d", s, v, fw, k)
	}
	if f.Volume().Server != nil || len(rep.KeptVolumes) != 1 || len(rep.Deleted) != 3 {
		t.Fatalf("report %+v", rep)
	}

	// up again: a new server gets the same data volume.
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if v := f.Volume(); v.ID != volID || v.Server == nil {
		t.Fatalf("the kept volume must be reattached: %+v", v)
	}

	if _, err := p.DestroyAll(ctx, true, quiet); err != nil {
		t.Fatal(err)
	}
	in, err := p.Inventory(ctx)
	if err != nil || !in.Empty() {
		t.Fatalf("labelled resources left: %+v %v", in, err)
	}
	if s, v, fw, k := f.Count(); s+v+fw+k != 0 {
		t.Fatal("everything must be gone")
	}
}

func TestEnsureRefusesForeignResources(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	f.AddForeignServer("shop")
	_, err := newProvider(t, f, nil).Ensure(context.Background(), quiet)
	if err == nil || !strings.Contains(err.Error(), "not made by Tiffin") {
		t.Fatalf("want a name clash error, got %v", err)
	}
}

func TestAdoptHandmadeServer(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	sid, vid := f.AddHandmadeServer("shiptiffin-server")
	own := filepath.Join(t.TempDir(), "shiptiffin_ed25519")
	pub, err := remote.GenerateKey(own, "owner")
	if err != nil {
		t.Fatal(err)
	}
	f.AddKey("shiptiffin-20261003", pub)
	p := newProvider(t, f, func(c *Config) { c.Name, c.KeyPath, c.OwnKey = "shiptiffin-server", own, true })

	pl, err := p.PlanAdopt(ctx, "shiptiffin-server", true)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(pl.Changes, "\n")
	for _, want := range []string{"auto-delete off", "label volume shiptiffin-server-vol", "create the firewall", "delete and rebuild protection", "never formatted"} {
		if !strings.Contains(all, want) {
			t.Errorf("plan lacks %q:\n%s", want, all)
		}
	}
	// cx23 4.99 + IPv4 0.61 + 40 GB 2.29
	if pl.ServerType != "cx23" || pl.Location != "nbg1" || pl.Arch != "amd64" || pl.MonthlyNet != 7.89 {
		t.Fatalf("plan %+v", pl)
	}
	if len(f.Mutations) != 0 {
		t.Fatalf("planning changed %v", f.Mutations)
	}
	if _, err := p.PlanAdopt(ctx, "nope", true); err == nil || !strings.Contains(err.Error(), "no server \"nope\"") {
		t.Fatalf("unknown server: %v", err)
	}

	// Adopt by ID, then converge like any box.
	if err := p.Adopt(ctx, strconv.FormatInt(sid, 10), true, quiet); err != nil {
		t.Fatal(err)
	}
	tg, err := p.Ensure(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	srv := f.ServerByName("shiptiffin-server")
	if srv.Labels["tiffin-box"] != "shiptiffin-server" || srv.Labels["owner"] != "me" || !srv.Protection.Delete || !srv.Protection.Rebuild {
		t.Fatalf("server after adopt: labels %v protection %+v", srv.Labels, srv.Protection)
	}
	v4 := f.PrimaryIP(srv.PublicNet.IPv4.ID)
	if v4.AutoDelete || !v4.Protection.Delete || v4.Labels["tiffin-box"] != "shiptiffin-server" {
		t.Fatalf("IPv4 after adopt: %+v", v4)
	}
	vol := f.Volume()
	if vol.ID != vid || vol.Labels["tiffin-box"] != "shiptiffin-server" || !vol.Protection.Delete {
		t.Fatalf("volume after adopt: %+v", vol)
	}
	if s, v, fw, k := f.Count(); s != 1 || v != 1 || fw != 1 || k != 1 {
		t.Fatalf("adopt must create only the firewall: %d %d %d %d", s, v, fw, k)
	}
	if fwl := f.Firewall(); len(fwl.AppliedTo) != 1 || fwl.AppliedTo[0].Server.ID != sid {
		t.Fatalf("firewall not attached: %+v", fwl.AppliedTo)
	}
	if tg.Host != v4.IP || tg.User != "root" || tg.Identity != own || p.VolumeDevice() != vol.LinuxDevice {
		t.Fatalf("target %+v device %s", tg, p.VolumeDevice())
	}
	if err := p.Adopt(ctx, "shiptiffin-server", true, quiet); err != nil {
		t.Fatalf("adopting again must be a no-op: %v", err)
	}
	other := newProvider(t, f, func(c *Config) { c.Name, c.KeyPath, c.OwnKey = "elsewhere", own, true })
	if _, err := other.PlanAdopt(ctx, "shiptiffin-server", true); err == nil || !strings.Contains(err.Error(), "already belongs to the Tiffin box shiptiffin-server") {
		t.Fatalf("a server of another box: %v", err)
	}

	// Down refuses protected resources unless told to remove the protection.
	if _, err := p.DestroyAll(ctx, false, quiet); err == nil || !strings.Contains(err.Error(), "--unprotect") {
		t.Fatalf("protected: %v", err)
	}
	if s, _, _, _ := f.Count(); s != 1 {
		t.Fatal("nothing may be deleted while protected")
	}
	p.Unprotect = true
	rep, err := p.DestroyAll(ctx, false, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if s, v, fw, _ := f.Count(); s != 0 || v != 1 || fw != 0 || len(rep.KeptIPs) != 1 || f.PrimaryIP(v4.ID) == nil {
		t.Fatalf("down keeps the volume and the IPv4: %+v", rep)
	}
	if _, err := p.DestroyAll(ctx, true, quiet); err != nil {
		t.Fatal(err)
	}
	if _, v, _, _ := f.Count(); v != 0 || f.IPCount() != 0 {
		t.Fatalf("--delete-data removes the volume and the IPv4: volumes %d ips %d", v, f.IPCount())
	}
}

func TestOwnKeyIsReusedNeverCopiedOrDeleted(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	dir := t.TempDir()
	own := filepath.Join(dir, "shiptiffin_ed25519")
	pub, err := remote.GenerateKey(own, "owner@laptop")
	if err != nil {
		t.Fatal(err)
	}
	f.AddKey("shiptiffin", pub) // uploaded by the owner in the console
	boxDir := t.TempDir()
	p := newProvider(t, f, func(c *Config) {
		c.KeyPath, c.OwnKey, c.KnownHosts = own, true, filepath.Join(boxDir, "known_hosts")
		c.Image = "ubuntu-26.04"
	})
	pl, err := p.Plan(ctx)
	if err != nil || pl.Image != "ubuntu-26.04" || !strings.Contains(strings.Join(pl.Create, "\n"), "Ubuntu 26.04") {
		t.Fatalf("plan %+v %v", pl, err)
	}
	tg, err := p.Ensure(ctx, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if tg.Identity != own {
		t.Fatalf("the own key must be used in place: %q", tg.Identity)
	}
	if _, _, _, k := f.Count(); k != 1 {
		t.Fatalf("the existing key must be reused, not uploaded again: %d keys", k)
	}
	for _, mu := range f.Mutations {
		if mu == "POST /ssh_keys" {
			t.Fatal("no key upload expected")
		}
	}
	if entries, _ := os.ReadDir(boxDir); len(entries) != 0 {
		t.Fatalf("nothing may be written next to the box for an own key: %v", entries)
	}
	if _, err := p.DestroyAll(ctx, true, quiet); err != nil {
		t.Fatal(err)
	}
	if _, _, _, k := f.Count(); k != 1 {
		t.Fatal("down must not delete a key Tiffin did not upload")
	}
	if _, err := New(Config{Token: "x", Name: "shop", Image: "debian-12"}); err == nil {
		t.Fatal("unsupported images must be refused")
	}
}
