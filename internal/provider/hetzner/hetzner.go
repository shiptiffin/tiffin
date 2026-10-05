// Package hetzner creates a Tiffin box on Hetzner Cloud with the official
// hcloud-go client: a server (Ubuntu 26.04, public IPv4 and IPv6), a Volume
// for /var/lib/tiffin that outlives the server, a Cloud Firewall and an SSH
// key made on this computer. Everything it makes carries two labels,
// tiffin=box and tiffin-box=<name>, so `tiffin down` deletes exactly that.
package hetzner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/provider"
	"github.com/btahir/tiffin/internal/provider/remote"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Defaults.
const (
	DefaultLocation   = "fsn1"
	DefaultServerType = "cax11"
	DefaultVolumeGB   = 40
	DefaultImage      = "ubuntu-26.04" // the image new servers get
	LabelKind         = "tiffin"       // tiffin=box on everything Tiffin makes
	LabelBox          = "tiffin-box"   // tiffin-box=<name>
)

// Config is one box on Hetzner.
type Config struct {
	Token      string
	Endpoint   string // API endpoint override (tests)
	Name       string // the box's name; resources are named after it
	Location   string // fsn1, nbg1, hel1, ash, hil, sin
	ServerType string // cax11 (ARM) by default; cx/cpx/ccx are x86
	VolumeGB   int
	// SSHFrom are this computer's addresses: added to the firewall's SSH
	// rule (with up to MaxSSHSources recent ones) before every connection.
	SSHFrom []netip.Prefix
	// SSHAnywhere opens port 22 to everyone (--ssh-from any).
	SSHAnywhere bool
	// Now is the clock (tests).
	Now func() time.Time
	// KeyPath is this box's private key (made on first up, mode 0600);
	// the public half is KeyPath+".pub".
	KeyPath string
	// OwnKey: KeyPath is the person's own key (--ssh-key): it is used as it
	// is and never generated, copied or deleted.
	OwnKey     bool
	KnownHosts string
	// Image is the Ubuntu release new servers get (DefaultImage).
	Image   string
	Version string // tiffin version, sent as the API user agent
	// PollInterval for actions (tests make it short).
	PollInterval time.Duration
}

// Provider manages one Hetzner box.
type Provider struct {
	cfg Config
	c   *hcloud.Client

	// Unprotect lets DestroyAll remove delete protection (adopted servers).
	Unprotect bool

	// Set by Up.
	SSH    SSHAccess // what the firewall's SSH rule allows now
	Server *hcloud.Server
	Volume *hcloud.Volume
}

var nameRE = func() func(string) bool {
	return func(s string) bool {
		if len(s) < 1 || len(s) > 40 {
			return false
		}
		for i, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' && i > 0 && i < len(s)-1) {
				return false
			}
		}
		return s[0] >= 'a' && s[0] <= 'z'
	}
}()

// ValidName reports whether name can name a box (and its server's hostname).
func ValidName(name string) error {
	if !nameRE(name) {
		return fmt.Errorf("box name %q: use 1-40 lowercase letters, digits and dashes, starting with a letter", name)
	}
	return nil
}

// New returns a provider. It makes no API calls.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("no Hetzner API token: set HCLOUD_TOKEN or pass --token-file (create one in the Hetzner Cloud console: your project → Security → API tokens, read & write)")
	}
	if err := ValidName(cfg.Name); err != nil {
		return nil, err
	}
	if cfg.Location == "" {
		cfg.Location = DefaultLocation
	}
	if cfg.ServerType == "" {
		cfg.ServerType = DefaultServerType
	}
	if cfg.VolumeGB == 0 {
		cfg.VolumeGB = DefaultVolumeGB
	}
	if cfg.Image == "" {
		cfg.Image = DefaultImage
	}
	if cfg.Image != DefaultImage {
		return nil, fmt.Errorf("image %q: Tiffin creates servers with %s", cfg.Image, DefaultImage)
	}
	if cfg.VolumeGB < 10 || cfg.VolumeGB > 10240 {
		return nil, fmt.Errorf("volume size %d GB: Hetzner volumes are 10 to 10240 GB", cfg.VolumeGB)
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	opts := []hcloud.ClientOption{
		hcloud.WithToken(strings.TrimSpace(cfg.Token)),
		hcloud.WithApplication("tiffin", cfg.Version),
		hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: hcloud.ConstantBackoff(cfg.PollInterval)}),
	}
	if cfg.Endpoint != "" {
		opts = append(opts, hcloud.WithEndpoint(cfg.Endpoint))
	}
	return &Provider{cfg: cfg, c: hcloud.NewClient(opts...)}, nil
}

func (p *Provider) Name() string  { return "hetzner" }
func (p *Provider) HostPort() int { return 443 }

// Labels are put on everything this box owns.
func (p *Provider) Labels() map[string]string {
	return map[string]string{LabelKind: "box", LabelBox: p.cfg.Name}
}

func (p *Provider) selector() string { return LabelKind + "=box," + LabelBox + "=" + p.cfg.Name }

// Inventory is what exists in the Hetzner project for this box.
type Inventory struct {
	Servers   []*hcloud.Server
	Volumes   []*hcloud.Volume
	Firewalls []*hcloud.Firewall
	SSHKeys   []*hcloud.SSHKey
	// PrimaryIPs carrying the box's labels (IPs made with the server are
	// deleted with it and carry none).
	PrimaryIPs []*hcloud.PrimaryIP
}

// Empty reports whether nothing labelled for the box exists.
func (in *Inventory) Empty() bool {
	return len(in.Servers)+len(in.Volumes)+len(in.Firewalls)+len(in.SSHKeys)+len(in.PrimaryIPs) == 0
}

// Inventory lists every resource labelled for this box.
func (p *Provider) Inventory(ctx context.Context) (*Inventory, error) {
	lo := hcloud.ListOpts{LabelSelector: p.selector()}
	var in Inventory
	var err error
	if in.Servers, err = p.c.Server.AllWithOpts(ctx, hcloud.ServerListOpts{ListOpts: lo}); err != nil {
		return nil, apiErr("list servers", err)
	}
	if in.Volumes, err = p.c.Volume.AllWithOpts(ctx, hcloud.VolumeListOpts{ListOpts: lo}); err != nil {
		return nil, apiErr("list volumes", err)
	}
	if in.Firewalls, err = p.c.Firewall.AllWithOpts(ctx, hcloud.FirewallListOpts{ListOpts: lo}); err != nil {
		return nil, apiErr("list firewalls", err)
	}
	if in.SSHKeys, err = p.c.SSHKey.AllWithOpts(ctx, hcloud.SSHKeyListOpts{ListOpts: lo}); err != nil {
		return nil, apiErr("list SSH keys", err)
	}
	if in.PrimaryIPs, err = p.c.PrimaryIP.AllWithOpts(ctx, hcloud.PrimaryIPListOpts{ListOpts: lo}); err != nil {
		return nil, apiErr("list primary IPs", err)
	}
	return &in, nil
}

// State reports whether the box's server exists and runs.
func (p *Provider) State(ctx context.Context) (provider.State, error) {
	in, err := p.Inventory(ctx)
	if err != nil {
		return provider.StateAbsent, err
	}
	if len(in.Servers) == 0 {
		return provider.StateAbsent, nil
	}
	if in.Servers[0].Status == hcloud.ServerStatusRunning {
		return provider.StateRunning, nil
	}
	return provider.StateStopped, nil
}

// GoArch maps a Hetzner architecture to Go's GOARCH.
func GoArch(a hcloud.Architecture) string {
	if a == hcloud.ArchitectureARM {
		return "arm64"
	}
	return "amd64"
}

// resolved is a validated server type in a location.
type resolved struct {
	st  *hcloud.ServerType
	loc *hcloud.Location
}

// resolve checks the server type exists and can be ordered in the location.
func (p *Provider) resolve(ctx context.Context) (*resolved, error) {
	loc, _, err := p.c.Location.GetByName(ctx, p.cfg.Location)
	if err != nil {
		return nil, apiErr("look up location", err)
	}
	if loc == nil {
		all, _ := p.c.Location.All(ctx)
		var names []string
		for _, l := range all {
			names = append(names, l.Name+" ("+l.City+")")
		}
		return nil, fmt.Errorf("unknown Hetzner location %q; choose one of: %s", p.cfg.Location, strings.Join(names, ", "))
	}
	st, _, err := p.c.ServerType.GetByName(ctx, p.cfg.ServerType)
	if err != nil {
		return nil, apiErr("look up server type", err)
	}
	if st == nil {
		return nil, fmt.Errorf("unknown Hetzner server type %q; try cax11 (ARM, 2 vCPU, 4 GB) or cx23 (x86)", p.cfg.ServerType)
	}
	if available, where := orderable(st, loc.Name); !available {
		hint := "no location offers it right now"
		if len(where) > 0 {
			hint = "it is available in " + strings.Join(where, ", ")
		}
		return nil, fmt.Errorf("server type %s (%s) cannot be ordered in %s: %s; pass --location or --type", st.Name, archWord(st.Architecture), loc.Name, hint)
	}
	return &resolved{st: st, loc: loc}, nil
}

// orderable reports whether st can be ordered in the location, and where it can.
func orderable(st *hcloud.ServerType, loc string) (bool, []string) {
	var where []string
	available := false
	for _, l := range st.Locations {
		if l.Location == nil {
			continue
		}
		ok := l.Available && !l.IsDeprecated()
		if ok {
			where = append(where, l.Location.Name)
		}
		if l.Location.Name == loc {
			available = ok
		}
	}
	if len(st.Locations) == 0 { // older API answers: fall back to pricing per location
		for _, pr := range st.Pricings {
			if pr.Location != nil {
				where = append(where, pr.Location.Name)
				available = available || pr.Location.Name == loc
			}
		}
	}
	sort.Strings(where)
	return available, where
}

func ubuntuName(image string) string {
	return "Ubuntu " + strings.TrimPrefix(image, "ubuntu-")
}

func archWord(a hcloud.Architecture) string {
	if a == hcloud.ArchitectureARM {
		return "ARM"
	}
	return "x86"
}

// ---- cost ----

// CostLine is one billed item.
type CostLine struct {
	What         string  `json:"what"`
	MonthlyNet   float64 `json:"monthlyNet"`
	MonthlyGross float64 `json:"monthlyGross"`
}

// Plan is what `up` would create, and what it costs per month.
type Plan struct {
	Name         string     `json:"name"`
	Image        string     `json:"image"`
	Location     string     `json:"location"`
	City         string     `json:"city"`
	ServerType   string     `json:"serverType"`
	Arch         string     `json:"arch"` // Go arch of the server: the tiffin build it needs
	Cores        int        `json:"cores"`
	MemoryGB     float32    `json:"memoryGB"`
	DiskGB       int        `json:"diskGB"`
	VolumeGB     int        `json:"volumeGB"`
	Currency     string     `json:"currency"`
	VATRate      string     `json:"vatRate"`
	Costs        []CostLine `json:"costs"`
	MonthlyNet   float64    `json:"monthlyNet"`
	MonthlyGross float64    `json:"monthlyGross"`
	Create       []string   `json:"create"` // resources that will be made
	Reuse        []string   `json:"reuse"`  // resources that already exist and are kept
	SSHFrom      []string   `json:"sshFrom"`
}

// Plan resolves and prices the box without creating anything (only GET
// requests): what --dry-run prints.
func (p *Provider) Plan(ctx context.Context) (*Plan, error) {
	r, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if img, _, err := p.c.Image.GetForArchitecture(ctx, p.cfg.Image, r.st.Architecture); err != nil {
		return nil, apiErr("look up the Ubuntu image", err)
	} else if img == nil {
		return nil, fmt.Errorf("no %s image on Hetzner for %s servers", p.cfg.Image, archWord(r.st.Architecture))
	}
	in, err := p.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	pricing, _, err := p.c.Pricing.Get(ctx)
	if err != nil {
		return nil, apiErr("read prices", err)
	}
	pl := &Plan{Name: p.cfg.Name, Image: p.cfg.Image, Location: r.loc.Name, City: r.loc.City, ServerType: r.st.Name, Arch: GoArch(r.st.Architecture),
		Cores: r.st.Cores, MemoryGB: r.st.Memory, DiskGB: r.st.Disk, VolumeGB: p.cfg.VolumeGB, Currency: pricing.Currency, VATRate: pricing.VATRate}
	pl.SSHFrom = sshFromWords(p.cfg.SSHFrom, p.cfg.SSHAnywhere)
	server := fmt.Sprintf("server %s (%s: %d vCPU %s, %g GB RAM, %d GB disk; %s; public IPv4 + IPv6)", p.cfg.Name, r.st.Name, r.st.Cores, archWord(r.st.Architecture), r.st.Memory, r.st.Disk, ubuntuName(p.cfg.Image))
	volume := fmt.Sprintf("volume %s-data (%d GB, XFS, mounted at /var/lib/tiffin, kept if the server is deleted)", p.cfg.Name, p.cfg.VolumeGB)
	firewall := fmt.Sprintf("firewall %s (in: SSH from %s; HTTP/HTTPS and ping from anywhere)", p.cfg.Name, strings.Join(pl.SSHFrom, ", "))
	key := fmt.Sprintf("SSH key tiffin-%s (made on this computer)", p.cfg.Name)
	if p.cfg.OwnKey {
		key = "SSH key from " + p.cfg.KeyPath + ".pub (reused if the project has it already)"
	}
	add := func(exists bool, what string) {
		if exists {
			pl.Reuse = append(pl.Reuse, what)
		} else {
			pl.Create = append(pl.Create, what)
		}
	}
	add(len(in.Servers) > 0, server)
	add(len(in.Volumes) > 0, volume)
	add(len(in.Firewalls) > 0, firewall)
	add(len(in.SSHKeys) > 0, key)

	stNet, stGross, ok := serverPrice(pricing, r.st.Name, r.loc.Name)
	if !ok {
		return nil, fmt.Errorf("no price on Hetzner for %s in %s", r.st.Name, r.loc.Name)
	}
	pl.Costs = append(pl.Costs, CostLine{What: "server " + r.st.Name, MonthlyNet: stNet, MonthlyGross: stGross})
	if n, g, ok := ipv4Price(pricing, r.loc.Name); ok {
		pl.Costs = append(pl.Costs, CostLine{What: "public IPv4 address", MonthlyNet: n, MonthlyGross: g})
	}
	vn, vg := VolumePrice(pricing, p.cfg.VolumeGB)
	pl.Costs = append(pl.Costs, CostLine{What: fmt.Sprintf("volume %d GB", p.cfg.VolumeGB), MonthlyNet: vn, MonthlyGross: vg})
	for _, c := range pl.Costs {
		pl.MonthlyNet += c.MonthlyNet
		pl.MonthlyGross += c.MonthlyGross
	}
	pl.MonthlyNet, pl.MonthlyGross = round2(pl.MonthlyNet), round2(pl.MonthlyGross)
	return pl, nil
}

func num(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func serverPrice(pr hcloud.Pricing, st, loc string) (float64, float64, bool) {
	for _, t := range pr.ServerTypes {
		if t.ServerType == nil || t.ServerType.Name != st {
			continue
		}
		for _, l := range t.Pricings {
			if l.Location != nil && l.Location.Name == loc {
				return num(l.Monthly.Net), num(l.Monthly.Gross), true
			}
		}
	}
	return 0, 0, false
}

// IPv4Price is the monthly price of a primary IPv4 address in a location.
func IPv4Price(pr hcloud.Pricing, l *hcloud.Location) (float64, float64, bool) {
	if l == nil {
		return 0, 0, false
	}
	return ipv4Price(pr, l.Name)
}

func ipv4Price(pr hcloud.Pricing, loc string) (float64, float64, bool) {
	for _, t := range pr.PrimaryIPs {
		if t.Type != "ipv4" {
			continue
		}
		for _, l := range t.Pricings {
			if l.Location == loc || strings.HasPrefix(l.Datacenter, loc+"-") {
				return num(l.Monthly.Net), num(l.Monthly.Gross), true
			}
		}
	}
	return 0, 0, false
}

// VolumePrice is the monthly price of a volume of gb.
func VolumePrice(pr hcloud.Pricing, gb int) (float64, float64) {
	return round2(num(pr.Volume.PerGBMonthly.Net) * float64(gb)), round2(num(pr.Volume.PerGBMonthly.Gross) * float64(gb))
}

// Prices returns Hetzner's price list (for down's leftover costs).
func (p *Provider) Prices(ctx context.Context) (hcloud.Pricing, error) {
	pr, _, err := p.c.Pricing.Get(ctx)
	return pr, apiErr("read prices", err)
}

// ---- up ----

// publicKey returns the public key line: the person's own key (OwnKey, read
// from KeyPath+".pub" and never generated) or the box's, made on first use.
func (p *Provider) publicKey() (string, error) {
	if !p.cfg.OwnKey {
		pub, err := remote.GenerateKey(p.cfg.KeyPath, "tiffin-"+p.cfg.Name)
		if err != nil {
			return "", fmt.Errorf("make the SSH key: %w", err)
		}
		return pub, nil
	}
	if _, err := os.Stat(p.cfg.KeyPath); err != nil {
		return "", fmt.Errorf("--ssh-key %s: %w", p.cfg.KeyPath, err)
	}
	raw, err := os.ReadFile(p.cfg.KeyPath + ".pub")
	if err != nil {
		return "", fmt.Errorf("--ssh-key needs the public key next to the private one: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// Up creates or reuses the box's key, firewall, volume and server, attaches
// the volume and waits until SSH answers. Re-running it changes nothing but
// the firewall's SSH source (the creator's current IP).
func (p *Provider) Up(ctx context.Context, progress func(string)) (provider.Machine, error) {
	m, err := p.UpServer(ctx, progress)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// UpServer is Up returning the concrete SSH machine.
func (p *Provider) UpServer(ctx context.Context, progress func(string)) (*remote.Machine, error) {
	t, err := p.Ensure(ctx, progress)
	if err != nil {
		return nil, err
	}
	m := remote.New(t)
	progress("waiting for SSH on " + t.Host)
	if err := m.Wait(ctx, 6*time.Minute); err != nil {
		if p.cfg.SSHAnywhere {
			return nil, err
		}
		return nil, fmt.Errorf("%w. The firewall allows SSH from %s; if this computer reaches the internet from another address, run: tiffin up --name %s --ssh-from any", err, strings.Join(sshFromWords(p.cfg.SSHFrom, false), ", "), p.cfg.Name)
	}
	return m, nil
}

// Ensure creates or reuses the box's cloud resources and returns how to
// reach its server over SSH. It does not connect.
func (p *Provider) Ensure(ctx context.Context, progress func(string)) (remote.Target, error) {
	var none remote.Target
	if len(p.cfg.SSHFrom) == 0 && !p.cfg.SSHAnywhere {
		return none, errors.New("no address to allow SSH from: Tiffin could not find this computer's public IP; pass --ssh-from <ip> (or --ssh-from any)")
	}
	if p.cfg.KeyPath == "" {
		return none, errors.New("no SSH key path")
	}
	in, err := p.Inventory(ctx)
	if err != nil {
		return none, err
	}
	if len(in.Servers) > 1 {
		return none, fmt.Errorf("%d servers are labelled %s=%s; Tiffin expects one. Delete the extra ones in the Hetzner console", len(in.Servers), LabelBox, p.cfg.Name)
	}
	var r *resolved
	if len(in.Servers) == 1 && in.Servers[0].ServerType != nil && in.Servers[0].Location != nil {
		// An existing (or adopted) server: its type and place are what they are.
		r = &resolved{st: in.Servers[0].ServerType, loc: in.Servers[0].Location}
	} else if r, err = p.resolve(ctx); err != nil {
		return none, err
	}
	if len(in.Volumes) > 1 {
		return none, fmt.Errorf("%d volumes are labelled %s=%s; Tiffin expects one", len(in.Volumes), LabelBox, p.cfg.Name)
	}

	// 1. The SSH key. Either the person's own (--ssh-key: only its public
	// half is read) or one made on this computer for the box. The private
	// half never leaves this computer.
	pub, err := p.publicKey()
	if err != nil {
		return none, err
	}
	fp, err := remote.Fingerprint(pub)
	if err != nil {
		return none, fmt.Errorf("%s.pub: %w", p.cfg.KeyPath, err)
	}
	var key *hcloud.SSHKey
	for _, k := range in.SSHKeys {
		if k.Fingerprint == fp {
			key = k
		}
	}
	if key == nil {
		// The same key may already be in the project under any name (the
		// person uploaded it in the console): use it, and never delete it.
		if k, _, err := p.c.SSHKey.GetByFingerprint(ctx, fp); err != nil {
			return none, apiErr("look up the SSH key", err)
		} else if k != nil {
			progress("using the SSH key " + k.Name + " already in the Hetzner project")
			key = k
		}
	}
	if key == nil && len(in.Servers) > 0 {
		return none, fmt.Errorf("the server %s exists but this computer does not have its SSH key (%s); run tiffin up from the computer that created it (or pass the same --ssh-key), or delete the box with tiffin down", in.Servers[0].Name, p.cfg.KeyPath)
	}
	if key == nil {
		for _, k := range in.SSHKeys { // ours, but for a key file that is gone
			if _, err := p.c.SSHKey.Delete(ctx, k); err != nil {
				return none, apiErr("delete the old SSH key", err)
			}
		}
		progress("uploading the SSH public key")
		if key, _, err = p.c.SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{Name: "tiffin-" + p.cfg.Name, PublicKey: pub, Labels: p.Labels()}); err != nil {
			if hcloud.IsError(err, hcloud.ErrorCodeUniquenessError) {
				return none, fmt.Errorf("an SSH key named tiffin-%s already exists in this Hetzner project but is not labelled for this box; delete it in the console or pick another --name", p.cfg.Name)
			}
			return none, apiErr("upload the SSH key", err)
		}
	}

	// 2. The firewall: SSH from this computer (and a few recent addresses),
	// the web from anywhere. Updated before any SSH, so a new home IP never
	// locks the owner out.
	var fw *hcloud.Firewall
	if len(in.Firewalls) > 0 {
		fw = in.Firewalls[0]
	}
	p.SSH = planSSH(fw, p.cfg.SSHFrom, p.cfg.SSHAnywhere, p.now())
	rules := p.firewallRules(p.SSH)
	if fw != nil {
		if p.SSH.Changed || !sameRules(fw.Rules, rules) {
			progress("updating the firewall: " + p.SSH.Summary())
			acts, _, err := p.c.Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{Rules: rules})
			if err != nil {
				return none, apiErr("update the firewall", err)
			}
			if err := p.wait(ctx, acts...); err != nil {
				return none, err
			}
		}
	} else {
		p.SSH.Changed = true
		progress("creating the firewall: " + p.SSH.Summary() + "; HTTP/HTTPS from anywhere")
		res, _, err := p.c.Firewall.Create(ctx, hcloud.FirewallCreateOpts{Name: p.cfg.Name, Labels: p.Labels(), Rules: rules})
		if err != nil {
			if hcloud.IsError(err, hcloud.ErrorCodeUniquenessError) {
				return none, fmt.Errorf("a firewall named %s already exists in this Hetzner project but is not labelled for this box; pick another --name", p.cfg.Name)
			}
			return none, apiErr("create the firewall", err)
		}
		if err := p.wait(ctx, res.Actions...); err != nil {
			return none, err
		}
		fw = res.Firewall
	}

	// 3. The volume: the box's data, kept when the server goes.
	var vol *hcloud.Volume
	if len(in.Volumes) > 0 {
		vol = in.Volumes[0]
		if vol.Location != nil && vol.Location.Name != r.loc.Name {
			return none, fmt.Errorf("this box's data volume is in %s, but the server would be in %s; volumes cannot move, so pass --location %s", vol.Location.Name, r.loc.Name, vol.Location.Name)
		}
	} else {
		progress(fmt.Sprintf("creating the %d GB data volume in %s", p.cfg.VolumeGB, r.loc.Name))
		res, _, err := p.c.Volume.Create(ctx, hcloud.VolumeCreateOpts{Name: p.cfg.Name + "-data", Size: p.cfg.VolumeGB, Location: r.loc, Labels: p.Labels()})
		if err != nil {
			if hcloud.IsError(err, hcloud.ErrorCodeUniquenessError) {
				return none, fmt.Errorf("a volume named %s-data already exists in this Hetzner project but is not labelled for this box; pick another --name", p.cfg.Name)
			}
			return none, apiErr("create the volume", err)
		}
		if err := p.wait(ctx, append([]*hcloud.Action{res.Action}, res.NextActions...)...); err != nil {
			return none, err
		}
		vol = res.Volume
	}

	// 4. The server.
	var srv *hcloud.Server
	if len(in.Servers) > 0 {
		srv = in.Servers[0]
		if srv.Status == hcloud.ServerStatusOff {
			progress("starting the server (it was off)")
			act, _, err := p.c.Server.Poweron(ctx, srv)
			if err != nil {
				return none, apiErr("power on the server", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return none, err
			}
		}
		if !firewallApplied(fw, srv.ID) {
			acts, _, err := p.c.Firewall.ApplyResources(ctx, fw, []hcloud.FirewallResource{{Type: hcloud.FirewallResourceTypeServer, Server: &hcloud.FirewallResourceServer{ID: srv.ID}}})
			if err != nil {
				return none, apiErr("apply the firewall", err)
			}
			if err := p.wait(ctx, acts...); err != nil {
				return none, err
			}
		}
	} else {
		img, _, err := p.c.Image.GetForArchitecture(ctx, p.cfg.Image, r.st.Architecture)
		if err != nil {
			return none, apiErr("look up the Ubuntu image", err)
		}
		if img == nil {
			return none, fmt.Errorf("no %s image on Hetzner for %s servers", p.cfg.Image, archWord(r.st.Architecture))
		}
		progress(fmt.Sprintf("creating the server %s (%s in %s)", p.cfg.Name, r.st.Name, r.loc.Name))
		opts := hcloud.ServerCreateOpts{
			Name: p.cfg.Name, ServerType: r.st, Image: img, Location: r.loc, SSHKeys: []*hcloud.SSHKey{key},
			Labels:    p.Labels(),
			PublicNet: &hcloud.ServerCreatePublicNet{EnableIPv4: true, EnableIPv6: true},
			Firewalls: []*hcloud.ServerCreateFirewall{{Firewall: *fw}},
			Automount: hcloud.Ptr(false),
		}
		if vol.Server == nil {
			opts.Volumes = []*hcloud.Volume{vol}
		}
		res, _, err := p.c.Server.Create(ctx, opts)
		if err != nil {
			if hcloud.IsError(err, hcloud.ErrorCodeUniquenessError) {
				return none, fmt.Errorf("a server named %s already exists in this Hetzner project but was not made by Tiffin; pick another --name", p.cfg.Name)
			}
			return none, apiErr("create the server", err)
		}
		if err := p.wait(ctx, append([]*hcloud.Action{res.Action}, res.NextActions...)...); err != nil {
			return none, err
		}
		srv = res.Server
	}
	if srv, _, err = p.c.Server.GetByID(ctx, srv.ID); err != nil || srv == nil {
		return none, apiErr("read the server", err)
	}

	// 5. The volume belongs to this server.
	if vol, _, err = p.c.Volume.GetByID(ctx, vol.ID); err != nil || vol == nil {
		return none, apiErr("read the volume", err)
	}
	switch {
	case vol.Server == nil:
		progress("attaching the data volume")
		act, _, err := p.c.Volume.AttachWithOpts(ctx, vol, hcloud.VolumeAttachOpts{Server: srv, Automount: hcloud.Ptr(false)})
		if err != nil {
			return none, apiErr("attach the volume", err)
		}
		if err := p.wait(ctx, act); err != nil {
			return none, err
		}
	case vol.Server.ID != srv.ID:
		return none, fmt.Errorf("the data volume %s is attached to another server (id %d); detach it in the Hetzner console first", vol.Name, vol.Server.ID)
	}
	p.Server, p.Volume = srv, vol

	ip4, ip6 := PublicIPs(srv)
	host := ip4
	if host == "" || len(p.cfg.SSHFrom) > 0 && p.cfg.SSHFrom[0].Addr().Is6() {
		host = ip6
	}
	if host == "" {
		return none, errors.New("the server has no public IP address")
	}
	if len(in.Servers) == 0 && p.cfg.KnownHosts != "" {
		_ = remote.ForgetHost(p.cfg.KnownHosts, ip4, ip6) // a new server may reuse an old IP
	}
	return remote.Target{User: "root", Host: host, Port: 22, Identity: p.cfg.KeyPath, KnownHosts: p.cfg.KnownHosts}, nil
}

func sshFromWords(from []netip.Prefix, anywhere bool) []string {
	if anywhere {
		return []string{"anywhere"}
	}
	var out []string
	for _, s := range from {
		out = append(out, SSHSource{Prefix: s}.String())
	}
	return out
}

// PublicIPs returns the server's IPv4 and its IPv6 (the ::1 of its /64).
func PublicIPs(s *hcloud.Server) (string, string) {
	var v4, v6 string
	if !s.PublicNet.IPv4.IsUnspecified() {
		v4 = s.PublicNet.IPv4.IP.String()
	}
	if n := s.PublicNet.IPv6.Network; n != nil {
		if a, ok := netip.AddrFromSlice(n.IP); ok {
			v6 = a.Next().String()
		}
	} else if ip := s.PublicNet.IPv6.IP; ip != nil && !ip.IsUnspecified() {
		if a, ok := netip.AddrFromSlice(ip); ok {
			v6 = a.Next().String()
		}
	}
	return v4, v6
}

// VolumeDevice is the stable device path of the box's volume on its server.
func (p *Provider) VolumeDevice() string {
	if p.Volume == nil {
		return ""
	}
	if p.Volume.LinuxDevice != "" {
		return p.Volume.LinuxDevice
	}
	return fmt.Sprintf("/dev/disk/by-id/scsi-0HC_Volume_%d", p.Volume.ID)
}

var (
	anyV4 = net.IPNet{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}
	anyV6 = net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
)

func (p *Provider) now() time.Time {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now()
}

func (p *Provider) firewallRules(a SSHAccess) []hcloud.FirewallRule {
	all := []net.IPNet{anyV4, anyV6}
	desc := func(s string) *string { return &s }
	return []hcloud.FirewallRule{
		sshRule(a),
		{Direction: hcloud.FirewallRuleDirectionIn, Protocol: hcloud.FirewallRuleProtocolTCP, Port: hcloud.Ptr("80"), SourceIPs: all, Description: desc("HTTP (redirects to HTTPS)")},
		{Direction: hcloud.FirewallRuleDirectionIn, Protocol: hcloud.FirewallRuleProtocolTCP, Port: hcloud.Ptr("443"), SourceIPs: all, Description: desc("HTTPS")},
		{Direction: hcloud.FirewallRuleDirectionIn, Protocol: hcloud.FirewallRuleProtocolICMP, SourceIPs: all, Description: desc("ping")},
	}
}

func ruleKey(r hcloud.FirewallRule) string {
	var src []string
	for _, n := range r.SourceIPs {
		src = append(src, n.String())
	}
	sort.Strings(src)
	port := ""
	if r.Port != nil {
		port = *r.Port
	}
	return string(r.Direction) + "|" + string(r.Protocol) + "|" + port + "|" + strings.Join(src, ",")
}

func sameRules(a, b []hcloud.FirewallRule) bool {
	if len(a) != len(b) {
		return false
	}
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		ka[i], kb[i] = ruleKey(a[i]), ruleKey(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	return slices.Equal(ka, kb)
}

func firewallApplied(fw *hcloud.Firewall, serverID int64) bool {
	for _, r := range fw.AppliedTo {
		if r.Server != nil && r.Server.ID == serverID {
			return true
		}
	}
	return false
}

func (p *Provider) wait(ctx context.Context, acts ...*hcloud.Action) error {
	if err := p.c.Action.WaitFor(ctx, acts...); err != nil {
		return apiErr("wait for Hetzner", err)
	}
	return nil
}

// Reboot reboots the server (an ACPI reboot, like `reboot` on it).
func (p *Provider) Reboot(ctx context.Context) error {
	in, err := p.Inventory(ctx)
	if err != nil {
		return err
	}
	if len(in.Servers) == 0 {
		return errors.New("no server")
	}
	act, _, err := p.c.Server.Reboot(ctx, in.Servers[0])
	if err != nil {
		return apiErr("reboot", err)
	}
	return p.wait(ctx, act)
}

// ---- down ----

// Destroy deletes everything labelled for the box except the data volume.
func (p *Provider) Destroy(ctx context.Context) error {
	_, err := p.DestroyAll(ctx, false, func(string) {})
	return err
}

// DestroyReport says what down did and what is left.
type DestroyReport struct {
	Deleted []string `json:"deleted"`
	Kept    []string `json:"kept,omitempty"`
	// KeptVolumes are volumes left in place (no --delete-data); they cost
	// money until deleted.
	KeptVolumes []*hcloud.Volume `json:"-"`
	// KeptIPs are primary IPs with auto-delete off (an adopted server's IPv4).
	KeptIPs []*hcloud.PrimaryIP `json:"-"`
}

// DestroyAll deletes the server, firewall and SSH key, and the volume only
// when deleteData is set (otherwise it is detached and kept).
func (p *Provider) DestroyAll(ctx context.Context, deleteData bool, progress func(string)) (*DestroyReport, error) {
	in, err := p.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	rep := &DestroyReport{}
	if prot := Protected(in, deleteData); len(prot) > 0 {
		if !p.Unprotect {
			return rep, fmt.Errorf("%s %s delete protection (an adopted server is protected); add --unprotect to remove it and delete", strings.Join(prot, ", "), map[bool]string{true: "has", false: "have"}[len(prot) == 1])
		}
		progress("removing delete protection from " + strings.Join(prot, ", "))
		if err := p.unprotect(ctx, in, deleteData); err != nil {
			return rep, err
		}
	}
	for _, s := range in.Servers {
		progress("deleting the server " + s.Name)
		res, _, err := p.c.Server.DeleteWithResult(ctx, s)
		if err != nil && !hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return rep, apiErr("delete the server", err)
		}
		if res != nil {
			if err := p.wait(ctx, res.Action); err != nil {
				return rep, err
			}
		}
		rep.Deleted = append(rep.Deleted, "server "+s.Name)
	}
	for _, f := range in.Firewalls {
		progress("deleting the firewall " + f.Name)
		// A deleted server releases its firewall a moment later.
		if err := retry(ctx, 10, func() error {
			_, err := p.c.Firewall.Delete(ctx, f)
			if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
				return nil
			}
			return err
		}); err != nil {
			return rep, apiErr("delete the firewall", err)
		}
		rep.Deleted = append(rep.Deleted, "firewall "+f.Name)
	}
	for _, k := range in.SSHKeys {
		if _, err := p.c.SSHKey.Delete(ctx, k); err != nil && !hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return rep, apiErr("delete the SSH key", err)
		}
		rep.Deleted = append(rep.Deleted, "SSH key "+k.Name)
	}
	for _, ip := range in.PrimaryIPs {
		if !ip.AutoDelete && !deleteData {
			// The address DNS points at: kept like the data (it costs while unassigned).
			rep.Kept = append(rep.Kept, "IPv4 address "+ip.IP.String()+" (id "+strconv.FormatInt(ip.ID, 10)+")")
			rep.KeptIPs = append(rep.KeptIPs, ip)
			continue
		}
		if _, err := p.c.PrimaryIP.Delete(ctx, ip); err != nil && !hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return rep, apiErr("delete the primary IP", err)
		}
		rep.Deleted = append(rep.Deleted, "primary IP "+ip.IP.String())
	}
	for _, v := range in.Volumes {
		if v, _, err = p.c.Volume.GetByID(ctx, v.ID); err != nil || v == nil {
			if err != nil {
				return rep, apiErr("read the volume", err)
			}
			continue
		}
		if v.Server != nil {
			act, _, err := p.c.Volume.Detach(ctx, v)
			if err != nil && !hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
				return rep, apiErr("detach the volume", err)
			}
			if err := p.wait(ctx, act); err != nil {
				return rep, err
			}
		}
		if !deleteData {
			rep.Kept = append(rep.Kept, fmt.Sprintf("volume %s (%d GB, id %d): your data", v.Name, v.Size, v.ID))
			rep.KeptVolumes = append(rep.KeptVolumes, v)
			continue
		}
		progress("deleting the data volume " + v.Name)
		if err := retry(ctx, 10, func() error {
			_, err := p.c.Volume.Delete(ctx, v)
			if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
				return nil
			}
			return err
		}); err != nil {
			return rep, apiErr("delete the volume", err)
		}
		rep.Deleted = append(rep.Deleted, "volume "+v.Name)
	}
	return rep, nil
}

func retry(ctx context.Context, n int, fn func() error) error {
	var err error
	for i := range n {
		if err = fn(); err == nil {
			return nil
		}
		if !hcloud.IsError(err, hcloud.ErrorCodeResourceInUse) && !hcloud.IsError(err, hcloud.ErrorCodeLocked) && !hcloud.IsError(err, hcloud.ErrorCodeConflict) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	return err
}

// apiErr turns an hcloud error into plain words.
func apiErr(what string, err error) error {
	if err == nil {
		return nil
	}
	var he hcloud.Error
	if errors.As(err, &he) {
		switch he.Code {
		case hcloud.ErrorCodeUnauthorized:
			return fmt.Errorf("%s: Hetzner rejected the API token (check HCLOUD_TOKEN: it must be a read & write token of the project)", what)
		case hcloud.ErrorCodeForbidden:
			return fmt.Errorf("%s: the API token is read-only; create a read & write token", what)
		case hcloud.ErrorCodeResourceLimitExceeded:
			return fmt.Errorf("%s: your Hetzner project hit a resource limit (%s); ask Hetzner to raise it or delete something", what, he.Message)
		case hcloud.ErrorCodeResourceUnavailable, hcloud.ErrorCodePlacementError:
			return fmt.Errorf("%s: Hetzner has no capacity for this right now (%s); try another --location or --type", what, he.Message)
		}
		return fmt.Errorf("%s: Hetzner said %s (%s)", what, he.Message, he.Code)
	}
	return fmt.Errorf("%s: %w", what, err)
}
