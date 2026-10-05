package hetzner

// Upgrading a box in place: a bigger (or smaller) server type and a bigger
// data volume. The data lives on the volume, so a type change keeps the
// server's own disk as it is (upgrade_disk=false) and a smaller type stays
// possible later.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Offer is a server type with its monthly price in the box's location.
type Offer struct {
	Name         string  `json:"name"`
	Arch         string  `json:"arch"` // Go arch: arm64, amd64
	Cores        int     `json:"cores"`
	MemoryGB     float32 `json:"memoryGB"`
	DiskGB       int     `json:"diskGB"`
	Dedicated    bool    `json:"dedicated,omitempty"` // dedicated vCPUs (ccx)
	MonthlyNet   float64 `json:"monthlyNet"`
	MonthlyGross float64 `json:"monthlyGross"`
}

// Words describes the type: "cax21 (4 vCPU ARM, 8 GB RAM, 80 GB disk)".
func (o Offer) Words() string {
	arch := "x86"
	if o.Arch == "arm64" {
		arch = "ARM"
	}
	if o.Dedicated {
		arch += " dedicated"
	}
	return fmt.Sprintf("%s (%d vCPU %s, %g GB RAM, %d GB disk)", o.Name, o.Cores, arch, o.MemoryGB, o.DiskGB)
}

func offer(st *hcloud.ServerType, pr hcloud.Pricing, loc string) Offer {
	o := Offer{Name: st.Name, Arch: GoArch(st.Architecture), Cores: st.Cores, MemoryGB: st.Memory, DiskGB: st.Disk, Dedicated: st.CPUType == hcloud.CPUTypeDedicated}
	o.MonthlyNet, o.MonthlyGross, _ = serverPrice(pr, st.Name, loc)
	return o
}

// Refusal is a resize Tiffin will not do, in words the person can act on.
type Refusal struct{ Msg string }

func (r *Refusal) Error() string { return r.Msg }

// Resize is what `tiffin up --type/--volume-size` changes on an existing box.
type Resize struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	Currency string `json:"currency"`
	VATRate  string `json:"vatRate"`
	From     Offer  `json:"from"`
	// To is the new server type; nil when the type stays.
	To *Offer `json:"to,omitempty"`
	// ServerDiskGB is the server's own disk, kept as it is on a type change.
	ServerDiskGB int `json:"serverDiskGB"`
	VolumeFromGB int `json:"volumeFromGB"`
	VolumeToGB   int `json:"volumeToGB"`
	// Monthly prices before and after (server, IPv4 and volume).
	MonthlyNetBefore   float64  `json:"monthlyNetBefore"`
	MonthlyNetAfter    float64  `json:"monthlyNetAfter"`
	MonthlyGrossBefore float64  `json:"monthlyGrossBefore"`
	MonthlyGrossAfter  float64  `json:"monthlyGrossAfter"`
	Steps              []string `json:"steps"`
	// Downtime: the server restarts (a type change); growing the volume alone has none.
	Downtime bool `json:"downtime"`
}

// Empty reports whether nothing changes.
func (r *Resize) Empty() bool { return r.To == nil && r.VolumeToGB == r.VolumeFromGB }

// existing returns the box's one server and its volume (nil if none).
func (p *Provider) existing(ctx context.Context) (*hcloud.Server, *hcloud.Volume, error) {
	in, err := p.Inventory(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Servers) != 1 || in.Servers[0].ServerType == nil || in.Servers[0].Location == nil {
		return nil, nil, fmt.Errorf("the box %s has %d servers labelled for it on Hetzner; Tiffin resizes a box with exactly one", p.cfg.Name, len(in.Servers))
	}
	var vol *hcloud.Volume
	if len(in.Volumes) > 0 {
		vol = in.Volumes[0]
	}
	return in.Servers[0], vol, nil
}

// PlanResize prices changing the box's server to serverType ("" keeps it)
// and growing its volume to volumeGB (0 keeps it). It only reads. Changes
// Tiffin will not make come back as a *Refusal.
func (p *Provider) PlanResize(ctx context.Context, serverType string, volumeGB int) (*Resize, error) {
	srv, vol, err := p.existing(ctx)
	if err != nil {
		return nil, err
	}
	pricing, _, err := p.c.Pricing.Get(ctx)
	if err != nil {
		return nil, apiErr("read prices", err)
	}
	loc := srv.Location.Name
	r := &Resize{Name: p.cfg.Name, Location: loc, Currency: pricing.Currency, VATRate: pricing.VATRate,
		From: offer(srv.ServerType, pricing, loc), ServerDiskGB: srv.PrimaryDiskSize}
	if r.ServerDiskGB == 0 {
		r.ServerDiskGB = srv.ServerType.Disk
	}
	if vol != nil {
		r.VolumeFromGB, r.VolumeToGB = vol.Size, vol.Size
	}

	if serverType != "" && serverType != srv.ServerType.Name {
		st, _, err := p.c.ServerType.GetByName(ctx, serverType)
		if err != nil {
			return nil, apiErr("look up server type", err)
		}
		if st == nil {
			return nil, &Refusal{fmt.Sprintf("unknown Hetzner server type %q; %s", serverType, p.choices(ctx, srv, pricing))}
		}
		if st.Architecture != srv.ServerType.Architecture {
			return nil, &Refusal{fmt.Sprintf("%s is %s and %s is %s: Hetzner cannot move a server between ARM and x86 (its system disk and Tiffin's build are made for one). %s. "+
				"To move to %s anyway, make a new box and move your projects there with tiffin box export and tiffin box import",
				srv.ServerType.Name, archWord(srv.ServerType.Architecture), st.Name, archWord(st.Architecture), p.choices(ctx, srv, pricing), archWord(st.Architecture))}
		}
		if ok, where := orderable(st, loc); !ok {
			hint := "no location offers it right now"
			if len(where) > 0 {
				hint = "it is offered in " + strings.Join(where, ", ")
			}
			return nil, &Refusal{fmt.Sprintf("%s cannot be ordered in %s, where this box is (%s). %s", st.Name, loc, hint, p.choices(ctx, srv, pricing))}
		}
		if st.Disk < r.ServerDiskGB {
			return nil, &Refusal{fmt.Sprintf("this server's disk is %d GB and %s has only %d GB: Hetzner cannot shrink a server's disk. %s",
				r.ServerDiskGB, st.Name, st.Disk, p.choices(ctx, srv, pricing))}
		}
		to := offer(st, pricing, loc)
		r.To, r.Downtime = &to, true
	}
	if volumeGB != 0 && vol != nil && volumeGB != vol.Size {
		if volumeGB < vol.Size {
			return nil, &Refusal{fmt.Sprintf("the data volume is %d GB; volumes cannot shrink (Hetzner grows them only). Keep it, or make a new box with a smaller --volume-size and move your projects there with tiffin box export and tiffin box import", vol.Size)}
		}
		if volumeGB > 10240 {
			return nil, &Refusal{fmt.Sprintf("volume size %d GB: Hetzner volumes are at most 10240 GB", volumeGB)}
		}
		r.VolumeToGB = volumeGB
	}

	ipNet, ipGross, _ := ipv4Price(pricing, loc)
	total := func(o Offer, gb int) (float64, float64) {
		vn, vg := VolumePrice(pricing, gb)
		return round2(o.MonthlyNet + ipNet + vn), round2(o.MonthlyGross + ipGross + vg)
	}
	r.MonthlyNetBefore, r.MonthlyGrossBefore = total(r.From, r.VolumeFromGB)
	after := r.From
	if r.To != nil {
		after = *r.To
	}
	r.MonthlyNetAfter, r.MonthlyGrossAfter = total(after, r.VolumeToGB)

	if r.VolumeToGB != r.VolumeFromGB {
		r.Steps = append(r.Steps, fmt.Sprintf("grow the data volume from %d to %d GB, then its filesystem, while everything keeps running (volumes cannot shrink later)", r.VolumeFromGB, r.VolumeToGB))
	}
	if r.To != nil {
		r.Steps = append(r.Steps,
			"stop Tiffin (apps finish their requests), shut the server down",
			fmt.Sprintf("change it from %s to %s; its own %d GB disk stays as it is, so you can go back to a smaller type later", r.From.Words(), r.To.Words(), r.ServerDiskGB),
			fmt.Sprintf("start it, retune Postgres, Valkey and the memory apps share for %g GB, and check HTTPS: about 2 minutes of downtime", r.To.MemoryGB))
		if srv.Protection.Delete || srv.Protection.Rebuild {
			r.Steps = append(r.Steps, "delete and rebuild protection stay on (they do not block a type change)")
		}
	}
	return r, nil
}

// choices names the types this box can change to: "ARM types you can
// change to here: cax21 (…) 7.99 EUR/month, …".
func (p *Provider) choices(ctx context.Context, srv *hcloud.Server, pricing hcloud.Pricing) string {
	all, err := p.c.ServerType.All(ctx)
	if err != nil {
		return "list the types with: tiffin up --name " + p.cfg.Name + " --dry-run"
	}
	opts := options(all, srv, pricing, false)
	if len(opts) == 0 {
		return "no other " + archWord(srv.ServerType.Architecture) + " type can be ordered in " + srv.Location.Name
	}
	var s []string
	for _, o := range opts[:min(len(opts), 6)] {
		s = append(s, fmt.Sprintf("%s %.2f %s/month", o.Words(), o.MonthlyNet, pricing.Currency))
	}
	return archWord(srv.ServerType.Architecture) + " types this box can change to: " + strings.Join(s, ", ")
}

// options are the types srv can change to: same architecture, orderable in
// its location, a disk at least as big as the server's, cheapest first.
// bigger keeps only types with more memory or more vCPUs.
func options(all []*hcloud.ServerType, srv *hcloud.Server, pricing hcloud.Pricing, bigger bool) []Offer {
	cur := srv.ServerType
	disk := srv.PrimaryDiskSize
	if disk == 0 {
		disk = cur.Disk
	}
	var out []Offer
	for _, st := range all {
		if st.Name == cur.Name || st.Architecture != cur.Architecture || st.Disk < disk {
			continue
		}
		if ok, _ := orderable(st, srv.Location.Name); !ok {
			continue
		}
		if bigger && st.Memory <= cur.Memory && st.Cores <= cur.Cores {
			continue
		}
		o := offer(st, pricing, srv.Location.Name)
		if o.MonthlyNet == 0 {
			continue // no price here
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MonthlyNet != out[j].MonthlyNet {
			return out[i].MonthlyNet < out[j].MonthlyNet
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Machine is what the dashboard shows about a Hetzner box's size: its
// server type, its volume and the next few bigger types, priced when
// `tiffin up` last ran (the box never holds the Hetzner token).
type Machine struct {
	ServerType  Offer     `json:"serverType"`
	Location    string    `json:"location"`
	VolumeGB    int       `json:"volumeGB"`
	Currency    string    `json:"currency"`
	VolumeGBNet float64   `json:"volumeGBMonthlyNet"` // price of one volume GB a month
	Upgrades    []Offer   `json:"upgrades"`
	PricedAt    time.Time `json:"pricedAt"`
}

// Machine describes the box's server and the next few bigger types (at most n).
func (p *Provider) Machine(ctx context.Context, n int) (*Machine, error) {
	srv, vol, err := p.existing(ctx)
	if err != nil {
		return nil, err
	}
	pricing, _, err := p.c.Pricing.Get(ctx)
	if err != nil {
		return nil, apiErr("read prices", err)
	}
	all, err := p.c.ServerType.All(ctx)
	if err != nil {
		return nil, apiErr("list server types", err)
	}
	m := &Machine{ServerType: offer(srv.ServerType, pricing, srv.Location.Name), Location: srv.Location.Name, Currency: pricing.Currency,
		VolumeGBNet: num(pricing.Volume.PerGBMonthly.Net), PricedAt: p.now().UTC()}
	if vol != nil {
		m.VolumeGB = vol.Size
	}
	up := options(all, srv, pricing, true)
	m.Upgrades = up[:min(len(up), n)]
	return m, nil
}

// ChangeType shuts the server down (an ACPI shutdown; powered off after
// two minutes if it does not stop), changes its type keeping its disk as it
// is, and starts it again. Delete and rebuild protection do not block it.
func (p *Provider) ChangeType(ctx context.Context, serverType string, progress func(string)) error {
	srv, _, err := p.existing(ctx)
	if err != nil {
		return err
	}
	st, _, err := p.c.ServerType.GetByName(ctx, serverType)
	if err != nil {
		return apiErr("look up server type", err)
	}
	if st == nil {
		return &Refusal{fmt.Sprintf("unknown Hetzner server type %q", serverType)}
	}
	if srv.Status != hcloud.ServerStatusOff {
		progress("shutting the server down")
		act, _, err := p.c.Server.Shutdown(ctx, srv)
		if err != nil {
			return apiErr("shut the server down", err)
		}
		if err := p.wait(ctx, act); err != nil {
			return err
		}
		if err := p.waitOff(ctx, srv, 2*time.Minute); err != nil {
			progress("the server did not shut down in 2 minutes; powering it off")
			act, _, err := p.c.Server.Poweroff(ctx, srv)
			if err != nil {
				return apiErr("power the server off", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return err
			}
		}
	}
	progress(fmt.Sprintf("changing the server from %s to %s", srv.ServerType.Name, st.Name))
	act, _, err := p.c.Server.ChangeType(ctx, srv, hcloud.ServerChangeTypeOpts{ServerType: st, UpgradeDisk: false})
	if err == nil {
		err = p.wait(ctx, act)
	}
	changeErr := err
	// Start the server whatever happened: a failed change leaves it as it was.
	progress("starting the server")
	act, _, err = p.c.Server.Poweron(ctx, srv)
	if err == nil {
		err = p.wait(ctx, act)
	}
	if changeErr != nil {
		return apiErr("change the server type (the server keeps "+srv.ServerType.Name+")", changeErr)
	}
	return apiErr("power the server on", err)
}

func (p *Provider) waitOff(ctx context.Context, srv *hcloud.Server, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		s, _, err := p.c.Server.GetByID(ctx, srv.ID)
		if err != nil {
			return apiErr("read the server", err)
		}
		if s != nil && s.Status == hcloud.ServerStatusOff {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(max(p.cfg.PollInterval, 10*time.Millisecond) * 2):
		}
	}
	return errors.New("the server is still running")
}

// GrowVolume grows the box's data volume to gb (online). The filesystem on
// it is grown separately, on the server (install.PrepareData).
func (p *Provider) GrowVolume(ctx context.Context, gb int, progress func(string)) error {
	_, vol, err := p.existing(ctx)
	if err != nil {
		return err
	}
	if vol == nil {
		return errors.New("the box has no data volume")
	}
	if gb <= vol.Size {
		return nil
	}
	progress(fmt.Sprintf("growing the data volume from %d to %d GB", vol.Size, gb))
	act, _, err := p.c.Volume.Resize(ctx, vol, gb)
	if err != nil {
		return apiErr("grow the volume", err)
	}
	return p.wait(ctx, act)
}
