package hetzner

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Adopting a server someone made by hand (in the Hetzner console) turns it
// into this box: its server, volume and IP addresses get the box's labels,
// the IPv4 address is kept if the server is ever deleted (DNS points at
// it), delete/rebuild protection goes on, and the box's Cloud Firewall is
// created and attached. Nothing is recreated and no disk is formatted; the
// volume moves from Hetzner's automount to /var/lib/tiffin only if it is
// empty (install.PrepareData). After Adopt, Ensure/UpServer treat the server
// like one Tiffin made.

// AdoptPlan is what adopting a server would change (--dry-run).
type AdoptPlan struct {
	Server       string     `json:"server"`
	ID           int64      `json:"id"`
	ServerType   string     `json:"serverType"`
	Location     string     `json:"location"`
	Arch         string     `json:"arch"`
	IPv4         string     `json:"ipv4,omitempty"`
	IPv6         string     `json:"ipv6,omitempty"`
	Volume       string     `json:"volume,omitempty"`
	Changes      []string   `json:"changes"`
	Currency     string     `json:"currency"`
	Costs        []CostLine `json:"costs" doc:"What the server already costs; adopting adds nothing (Cloud Firewalls are free)."`
	MonthlyNet   float64    `json:"monthlyNet"`
	MonthlyGross float64    `json:"monthlyGross"`
	SSHFrom      []string   `json:"sshFrom"`
}

// FindServer looks a server up by ID or name, and refuses one that belongs
// to another box.
func (p *Provider) FindServer(ctx context.Context, ref string) (*hcloud.Server, *hcloud.Volume, error) {
	s, _, err := p.c.Server.Get(ctx, ref)
	if err != nil {
		return nil, nil, apiErr("look up the server", err)
	}
	if s == nil {
		return nil, nil, fmt.Errorf("there is no server %q in this Hetzner project (give its name or ID)", ref)
	}
	if owner := s.Labels[LabelBox]; owner != "" && owner != p.cfg.Name {
		return nil, nil, fmt.Errorf("server %s already belongs to the Tiffin box %s; use --name %s", s.Name, owner, owner)
	}
	in, err := p.Inventory(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range in.Servers {
		if o.ID != s.ID {
			return nil, nil, fmt.Errorf("the box %s already has a server (%s); pick another --name", p.cfg.Name, o.Name)
		}
	}
	var vol *hcloud.Volume
	switch len(s.Volumes) {
	case 0:
	case 1:
		if vol, _, err = p.c.Volume.GetByID(ctx, s.Volumes[0].ID); err != nil {
			return nil, nil, apiErr("read the volume", err)
		}
	default:
		return nil, nil, fmt.Errorf("server %s has %d volumes attached; Tiffin adopts a server with at most one (detach the others first)", s.Name, len(s.Volumes))
	}
	return s, vol, nil
}

// PlanAdopt says what Adopt would change, without changing anything.
func (p *Provider) PlanAdopt(ctx context.Context, ref string, protect bool) (*AdoptPlan, error) {
	s, vol, err := p.FindServer(ctx, ref)
	if err != nil {
		return nil, err
	}
	in, err := p.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	pl := &AdoptPlan{Server: s.Name, ID: s.ID}
	if s.ServerType != nil {
		pl.ServerType, pl.Arch = s.ServerType.Name, GoArch(s.ServerType.Architecture)
	}
	if s.Location != nil {
		pl.Location = s.Location.Name
	}
	pl.IPv4, pl.IPv6 = PublicIPs(s)
	pl.SSHFrom = sshFromWords(p.cfg.SSHFrom, p.cfg.SSHAnywhere)
	add := func(f string, a ...any) { pl.Changes = append(pl.Changes, fmt.Sprintf(f, a...)) }
	if s.Labels[LabelBox] != p.cfg.Name {
		add("label server %s (id %d) %s=box, %s=%s", s.Name, s.ID, LabelKind, LabelBox, p.cfg.Name)
	}
	if id := s.PublicNet.IPv4.ID; id != 0 {
		if ip, _, err := p.c.PrimaryIP.GetByID(ctx, id); err == nil && ip != nil {
			if ip.AutoDelete {
				add("keep the IPv4 address %s if the server is ever deleted (auto-delete off), since DNS will point at it", ip.IP)
			}
			if ip.Labels[LabelBox] != p.cfg.Name {
				add("label the IPv4 address %s", ip.IP)
			}
		}
	}
	if vol != nil {
		if vol.Labels[LabelBox] != p.cfg.Name {
			add("label volume %s (%d GB, id %d)", vol.Name, vol.Size, vol.ID)
		}
		add("use volume %s for /var/lib/tiffin: moved from Hetzner's automount and labelled %q if it is empty; never formatted if it holds data", vol.Name, "tiffin-data")
		pl.Volume = fmt.Sprintf("%s (%d GB, id %d)", vol.Name, vol.Size, vol.ID)
	} else {
		add("create and attach a %d GB data volume (the server has none)", p.cfg.VolumeGB)
	}
	if len(in.Firewalls) == 0 {
		add("create the firewall %s (in: SSH from %s; HTTP/HTTPS and ping from anywhere) and attach it", p.cfg.Name, strings.Join(pl.SSHFrom, ", "))
	} else {
		acc := planSSH(in.Firewalls[0], p.cfg.SSHFrom, p.cfg.SSHAnywhere, p.now())
		add("firewall %s: %s", in.Firewalls[0].Name, acc.Summary())
	}
	if protect {
		if !s.Protection.Delete || !s.Protection.Rebuild {
			add("turn on delete and rebuild protection for the server")
		}
		add("turn on delete protection for the volume and the IPv4 address")
	}
	add("install Tiffin over SSH as root with %s, and harden the server", p.cfg.KeyPath)

	pricing, _, err := p.c.Pricing.Get(ctx)
	if err != nil {
		return nil, apiErr("read prices", err)
	}
	pl.Currency = pricing.Currency
	if n, g, ok := serverPrice(pricing, pl.ServerType, pl.Location); ok {
		pl.Costs = append(pl.Costs, CostLine{What: "server " + pl.ServerType, MonthlyNet: n, MonthlyGross: g})
	}
	if pl.IPv4 != "" {
		if n, g, ok := ipv4Price(pricing, pl.Location); ok {
			pl.Costs = append(pl.Costs, CostLine{What: "public IPv4 address", MonthlyNet: n, MonthlyGross: g})
		}
	}
	gb := p.cfg.VolumeGB
	if vol != nil {
		gb = vol.Size
	}
	n, g := VolumePrice(pricing, gb)
	pl.Costs = append(pl.Costs, CostLine{What: fmt.Sprintf("volume %d GB", gb), MonthlyNet: n, MonthlyGross: g})
	for _, c := range pl.Costs {
		pl.MonthlyNet += c.MonthlyNet
		pl.MonthlyGross += c.MonthlyGross
	}
	pl.MonthlyNet, pl.MonthlyGross = round2(pl.MonthlyNet), round2(pl.MonthlyGross)
	return pl, nil
}

// Adopt labels and protects an existing server (and its volume and IP
// addresses) as this box. Run UpServer next: it adds the firewall and
// connects. Idempotent.
func (p *Provider) Adopt(ctx context.Context, ref string, protect bool, progress func(string)) error {
	s, vol, err := p.FindServer(ctx, ref)
	if err != nil {
		return err
	}
	if _, err := p.publicKey(); err != nil {
		return err
	}
	merged := func(have map[string]string) map[string]string {
		m := maps.Clone(have)
		if m == nil {
			m = map[string]string{}
		}
		maps.Copy(m, p.Labels())
		return m
	}
	if s.Labels[LabelBox] != p.cfg.Name {
		progress("labelling the server " + s.Name)
		if _, _, err := p.c.Server.Update(ctx, s, hcloud.ServerUpdateOpts{Labels: merged(s.Labels)}); err != nil {
			return apiErr("label the server", err)
		}
	}
	if vol != nil && vol.Labels[LabelBox] != p.cfg.Name {
		progress("labelling the volume " + vol.Name)
		if _, _, err := p.c.Volume.Update(ctx, vol, hcloud.VolumeUpdateOpts{Labels: merged(vol.Labels)}); err != nil {
			return apiErr("label the volume", err)
		}
	}
	for i, id := range []int64{s.PublicNet.IPv4.ID, s.PublicNet.IPv6.ID} {
		if id == 0 {
			continue
		}
		ip, _, err := p.c.PrimaryIP.GetByID(ctx, id)
		if err != nil || ip == nil {
			return apiErr("read the primary IP", err)
		}
		opts := hcloud.PrimaryIPUpdateOpts{}
		if ip.Labels[LabelBox] != p.cfg.Name {
			l := merged(ip.Labels)
			opts.Labels = &l
		}
		if i == 0 && ip.AutoDelete { // keep the IPv4 DNS points at
			opts.AutoDelete = hcloud.Ptr(false)
		}
		if opts.Labels != nil || opts.AutoDelete != nil {
			progress("labelling the IP address " + ip.IP.String())
			if _, _, err := p.c.PrimaryIP.Update(ctx, ip, opts); err != nil {
				return apiErr("update the primary IP", err)
			}
		}
		if protect && i == 0 && !ip.Protection.Delete {
			act, _, err := p.c.PrimaryIP.ChangeProtection(ctx, hcloud.PrimaryIPChangeProtectionOpts{ID: ip.ID, Delete: true})
			if err != nil {
				return apiErr("protect the IPv4 address", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return err
			}
		}
	}
	if protect {
		if !s.Protection.Delete || !s.Protection.Rebuild {
			progress("turning on delete and rebuild protection")
			act, _, err := p.c.Server.ChangeProtection(ctx, s, hcloud.ServerChangeProtectionOpts{Delete: hcloud.Ptr(true), Rebuild: hcloud.Ptr(true)})
			if err != nil {
				return apiErr("protect the server", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return err
			}
		}
		if vol != nil && !vol.Protection.Delete {
			act, _, err := p.c.Volume.ChangeProtection(ctx, vol, hcloud.VolumeChangeProtectionOpts{Delete: hcloud.Ptr(true)})
			if err != nil {
				return apiErr("protect the volume", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return err
			}
		}
	}
	return nil
}

// unprotect removes delete protection from what DestroyAll is about to delete.
func (p *Provider) unprotect(ctx context.Context, in *Inventory, deleteData bool) error {
	for _, s := range in.Servers {
		if s.Protection.Delete || s.Protection.Rebuild {
			act, _, err := p.c.Server.ChangeProtection(ctx, s, hcloud.ServerChangeProtectionOpts{Delete: hcloud.Ptr(false), Rebuild: hcloud.Ptr(false)})
			if err != nil {
				return apiErr("remove the server's protection", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return err
			}
		}
	}
	if !deleteData {
		return nil
	}
	for _, v := range in.Volumes {
		if v.Protection.Delete {
			act, _, err := p.c.Volume.ChangeProtection(ctx, v, hcloud.VolumeChangeProtectionOpts{Delete: hcloud.Ptr(false)})
			if err != nil {
				return apiErr("remove the volume's protection", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return err
			}
		}
	}
	for _, ip := range in.PrimaryIPs {
		if ip.Protection.Delete {
			act, _, err := p.c.PrimaryIP.ChangeProtection(ctx, hcloud.PrimaryIPChangeProtectionOpts{ID: ip.ID, Delete: false})
			if err != nil {
				return apiErr("remove the IP address's protection", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return err
			}
		}
	}
	return nil
}

// Protected lists what delete protection guards among the resources down
// would delete.
func Protected(in *Inventory, deleteData bool) []string {
	var out []string
	for _, s := range in.Servers {
		if s.Protection.Delete {
			out = append(out, "server "+s.Name)
		}
	}
	if deleteData {
		for _, v := range in.Volumes {
			if v.Protection.Delete {
				out = append(out, "volume "+v.Name)
			}
		}
		for _, ip := range in.PrimaryIPs {
			if ip.Protection.Delete {
				out = append(out, "IP address "+ip.IP.String())
			}
		}
	}
	return out
}
