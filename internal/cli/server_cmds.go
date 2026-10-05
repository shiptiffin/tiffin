package cli

// `tiffin up` and `tiffin down` for boxes on real servers: Hetzner Cloud
// (Tiffin creates the server) and plain SSH (any Ubuntu 24.04 or 26.04 server).

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/install"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/provider/hetzner"
	"github.com/btahir/tiffin/internal/provider/remote"
	"github.com/btahir/tiffin/internal/version"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"
)

const sslipSuffix = ".sslip.io"

// sslipDomain is the box's domain until it has its own: the public IP
// spelled as a name ("203.0.113.5" → "203-0-113-5.sslip.io"), which
// sslip.io's DNS answers with that IP. The dashboard is dashboard.<domain>.
func sslipDomain(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	a = a.Unmap()
	if a.Is4() {
		return strings.ReplaceAll(a.String(), ".", "-") + sslipSuffix
	}
	return strings.ReplaceAll(a.String(), ":", "-") + sslipSuffix
}

// sslipAddr reads the IP back out of a sslip.io name, so the CLI can dial
// it without a DNS lookup.
func sslipAddr(host string) (netip.Addr, bool) {
	rest, ok := strings.CutSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), sslipSuffix)
	if !ok {
		return netip.Addr{}, false
	}
	if i := strings.LastIndex(rest, "."); i >= 0 {
		rest = rest[i+1:]
	}
	if a, err := netip.ParseAddr(strings.ReplaceAll(rest, "-", ".")); err == nil && a.Is4() {
		return a, true
	}
	if a, err := netip.ParseAddr(strings.ReplaceAll(rest, "-", ":")); err == nil && a.Is6() {
		return a, true
	}
	return netip.Addr{}, false
}

// publicIPLookup finds this computer's public addresses (for the Hetzner
// firewall's SSH rule). Tests replace it.
var publicIPLookup = func(ctx context.Context) []netip.Addr {
	var out []netip.Addr
	for _, u := range []string{"https://api.ipify.org", "https://api6.ipify.org"} {
		ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 100))
			res.Body.Close()
			if a, err := netip.ParseAddr(strings.TrimSpace(string(body))); err == nil && res.StatusCode == 200 {
				out = append(out, a.Unmap())
			}
		}
		cancel()
	}
	return out
}

// sshFrom are the addresses allowed to reach a Hetzner box's SSH port.
// "any" opens SSH to everyone (keys only; CrowdSec bans brute force).
func (a *app) sshFrom(ctx context.Context, flag []string) ([]netip.Prefix, bool, error) {
	var out []netip.Prefix
	for _, s := range flag {
		if strings.EqualFold(strings.TrimSpace(s), "any") {
			if len(flag) > 1 {
				return nil, false, &exitError{ExitInvalid, "--ssh-from any cannot be combined with addresses"}
			}
			return nil, true, nil
		}
		p, err := platform.ParseIPOrPrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, false, &exitError{ExitInvalid, "--ssh-from: " + err.Error()}
		}
		out = append(out, p)
	}
	if len(out) > 0 {
		return out, false, nil
	}
	a.progress("finding this computer's public IP (for the firewall's SSH rule)")
	for _, ip := range publicIPLookup(ctx) {
		p, _ := platform.ParseIPOrPrefix(remote.OwnerRange(ip.String()))
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, false, &exitError{ExitError, "could not find this computer's public IP to allow SSH from; pass --ssh-from <your ip>, or --ssh-from any"}
	}
	return out, false, nil
}

// hetznerToken reads the API token from a file or HCLOUD_TOKEN. It is never stored.
func (a *app) hetznerToken(file string) (string, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", &exitError{ExitInvalid, "--token-file: " + err.Error()}
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if t := strings.TrimSpace(a.io.Env("HCLOUD_TOKEN")); t != "" {
		return t, nil
	}
	return "", &exitError{ExitInvalid, "no Hetzner API token: set HCLOUD_TOKEN or pass --token-file (Hetzner Cloud console → your project → Security → API tokens → read & write)"}
}

func (a *app) hetznerProvider(name string, sb *serverBox, tokenFile string, sshFrom []netip.Prefix) (*hetzner.Provider, error) {
	anywhere := sb.SSHAnywhere
	token, err := a.hetznerToken(tokenFile)
	if err != nil {
		return nil, err
	}
	hp, err := hetzner.New(hetzner.Config{Token: token, Endpoint: a.io.Env("HCLOUD_ENDPOINT"), Name: name, Location: sb.Location, OwnKey: sb.OwnKey,
		ServerType: sb.ServerType, VolumeGB: sb.VolumeGB, SSHFrom: sshFrom, SSHAnywhere: anywhere, KeyPath: sb.Identity, KnownHosts: sb.KnownHosts, Version: version.Version})
	if err != nil {
		return nil, &exitError{ExitInvalid, err.Error()}
	}
	return hp, nil
}

// expandHome turns a leading ~/ into the home directory.
func (a *app) expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if h := a.io.Env("HOME"); h != "" {
			return filepath.Join(h, rest)
		}
	}
	return p
}

func pick(flag, old string) string {
	if flag != "" {
		return flag
	}
	return old
}

// addOwnerIP remembers the latest addresses the owner set the box up from (at most 5).
func addOwnerIP(list []string, ip string) []string {
	list = slices.DeleteFunc(slices.Clone(list), func(s string) bool { return s == ip })
	list = append(list, ip)
	if len(list) > 5 {
		list = list[len(list)-5:]
	}
	return list
}

// upServer creates or updates a box on a real server.
func (a *app) upServer(cmd *cobra.Command, prov string, o upOptions) error {
	ctx := cmd.Context()
	start := time.Now()
	name := pick(o.name, "tiffin")
	if o.adopt != "" && o.name == "" {
		if _, err := strconv.ParseInt(o.adopt, 10, 64); err == nil {
			return &exitError{ExitInvalid, "--adopt by ID needs --name for the box (e.g. the server's name)"}
		}
		name = o.adopt
	}
	if o.adopt != "" && prov != "hetzner" {
		return &exitError{ExitInvalid, "--adopt is for --provider hetzner (for any other server use --provider ssh)"}
	}
	if name == "local" {
		return &exitError{ExitInvalid, "local is the local box's name; pick another --name"}
	}
	if err := hetzner.ValidName(name); err != nil {
		return &exitError{ExitInvalid, err.Error()}
	}
	f, err := a.loadBoxes()
	if err != nil {
		return err
	}
	bx := f.Boxes[name]
	if bx != nil && bx.Provider != prov {
		return &exitError{ExitInvalid, fmt.Sprintf("the box %s is a %s box; pick another --name", name, bx.Provider)}
	}
	prev := &serverBox{}
	if bx != nil && bx.Server != nil {
		prev = bx.Server
	}
	window := prev.RebootWindow
	if cmd.Flags().Changed("reboot-window") {
		window = o.rebootWindow
	}
	if window == "off" {
		window = ""
	}
	if err := platform.ValidRebootWindow(window); err != nil {
		return &exitError{ExitInvalid, "--reboot-window: " + err.Error()}
	}
	if err := remote.Available(); err != nil {
		return &exitError{ExitError, err.Error()}
	}
	dir := filepath.Join(a.configDir(), "boxes", name)
	sb := &serverBox{KnownHosts: filepath.Join(dir, "known_hosts"), RebootWindow: window, OwnerIPs: prev.OwnerIPs}

	var m *remote.Machine
	var data install.DataSpec
	var plan *hetzner.Plan
	var sshAccess *hetzner.SSHAccess
	switch prov {
	case "hetzner":
		sb.Location = pick(o.location, pick(prev.Location, a.io.Env("HCLOUD_LOCATION")))
		sb.ServerType = pick(o.serverType, prev.ServerType)
		sb.VolumeGB = o.volumeGB
		if sb.VolumeGB == 0 {
			sb.VolumeGB = prev.VolumeGB
		}
		sb.TokenFile = pick(o.tokenFile, prev.TokenFile)
		sb.Identity, sb.OwnKey = filepath.Join(dir, "id_ed25519"), false
		if k := pick(o.sshKey, a.io.Env("HCLOUD_SSH_KEY")); k != "" {
			sb.Identity, sb.OwnKey = a.expandHome(k), true
		} else if prev.OwnKey {
			sb.Identity, sb.OwnKey = prev.Identity, true
		}
		var from []netip.Prefix
		sb.SSHAnywhere = prev.SSHAnywhere && len(o.sshFrom) == 0
		if !sb.SSHAnywhere {
			var anywhere bool
			if from, anywhere, err = a.sshFrom(ctx, o.sshFrom); err != nil {
				return err
			}
			sb.SSHAnywhere = anywhere
		}
		hp, err := a.hetznerProvider(name, sb, sb.TokenFile, from)
		if err != nil {
			return err
		}
		existing := bx != nil && bx.URL != ""
		switch {
		case o.adopt != "":
			if !sb.OwnKey {
				return &exitError{ExitInvalid, "--adopt needs the key that already logs in to the server as root: pass --ssh-key <private key> or set HCLOUD_SSH_KEY"}
			}
			ap, err := hp.PlanAdopt(ctx, o.adopt, !o.noProtect)
			if err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if o.dryRun {
				a.printAdoptPlan(ap)
				return nil
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := hp.Adopt(ctx, o.adopt, !o.noProtect, a.progress); err != nil {
				return &exitError{ExitError, err.Error()}
			}
			plan = &hetzner.Plan{Location: ap.Location, ServerType: ap.ServerType, Currency: ap.Currency, MonthlyNet: ap.MonthlyNet, MonthlyGross: ap.MonthlyGross}
			sb.Adopted = ap.ID
		case existing && !o.dryRun:
			// Updating a box: nothing new is created, no plan to price.
			sb.Adopted = prev.Adopted
		default:
			if plan, err = hp.Plan(ctx); err != nil {
				return &exitError{ExitError, err.Error()}
			}
			if o.dryRun {
				a.printPlan(plan)
				return nil
			}
			if len(plan.Create) > 0 {
				a.progress(fmt.Sprintf("creating %d Hetzner resources: about %.2f %s a month before VAT (%.2f with VAT)", len(plan.Create), plan.MonthlyNet, plan.Currency, plan.MonthlyGross))
			}
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if m, err = hp.UpServer(ctx, a.progress); err != nil {
			return &exitError{ExitError, err.Error()}
		}
		sb.PublicIP, sb.PublicIPv6 = hetzner.PublicIPs(hp.Server)
		sshAccess = &hp.SSH // the summary at the end says who may SSH in
		if sb.Ubuntu, err = m.Check(ctx); err != nil {
			return &exitError{ExitError, err.Error()}
		}
		sb.VolumeGB = hp.Volume.Size
		if hp.Server.ServerType != nil {
			sb.ServerType = hp.Server.ServerType.Name
		}
		if hp.Server.Location != nil {
			sb.Location = hp.Server.Location.Name
		}
		data = install.DataSpec{Device: hp.VolumeDevice()}
	case "ssh":
		host := pick(o.host, prev.SSH)
		if host == "" {
			return &exitError{ExitInvalid, "--host is required: the server as user@host (the user needs sudo)"}
		}
		t, err := remote.ParseTarget(host)
		if err != nil {
			return &exitError{ExitInvalid, err.Error()}
		}
		sb.Identity = pick(o.identity, pick(o.sshKey, prev.Identity))
		if sb.Identity != "" {
			sb.Identity = a.expandHome(sb.Identity)
			if abs, err := filepath.Abs(sb.Identity); err == nil {
				sb.Identity = abs
			}
		}
		t.Identity, t.KnownHosts = sb.Identity, sb.KnownHosts
		sb.DataDisk, sb.DataDir = pick(o.dataDisk, prev.DataDisk), pick(o.dataDir, prev.DataDir)
		if o.dataDisk != "" {
			sb.DataDir = ""
		} else if o.dataDir != "" {
			sb.DataDisk = ""
		}
		data = install.DataSpec{Device: sb.DataDisk, Dir: sb.DataDir}
		sb.PublicIP, sb.PublicIPv6 = pick(o.publicIP, prev.PublicIP), prev.PublicIPv6
		if o.publicIP != "" {
			sb.PublicIPv6 = ""
		}
		if sb.PublicIP == "" {
			if sb.PublicIP, sb.PublicIPv6, err = resolveHost(ctx, t.Host); err != nil {
				return &exitError{ExitInvalid, err.Error()}
			}
		}
		if ip, err := netip.ParseAddr(sb.PublicIP); err != nil {
			return &exitError{ExitInvalid, "--public-ip: " + err.Error()}
		} else if ip.Is6() {
			sb.PublicIP, sb.PublicIPv6 = "", ip.String()
		}
		sb.SSH = t.String()
		if o.dryRun {
			a.printSSHPlan(name, t, data, sb)
			return nil
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		m = remote.New(t)
		a.progress("connecting to " + t.String())
		if err := m.Wait(ctx, 2*time.Minute); err != nil {
			return &exitError{ExitError, err.Error()}
		}
		if sb.Ubuntu, err = m.Check(ctx); err != nil {
			return &exitError{ExitError, err.Error()}
		}
	}
	sb.SSH = m.T.String()

	// Remember the box now, so `tiffin down` can find it even if the install fails.
	if bx == nil {
		bx = &boxConfig{Provider: prov, CreatedAt: time.Now().UTC()}
		f.Boxes[name] = bx
	}
	bx.Server = sb
	if err := a.saveBoxes(f); err != nil {
		return err
	}

	// The address the server sees this computer connect from: never banned.
	if ip, err := m.ClientIP(ctx); err == nil {
		sb.OwnerIPs = addOwnerIP(sb.OwnerIPs, remote.OwnerRange(ip))
	}
	arch := m.Arch()
	if arch != "arm64" && arch != "amd64" {
		return &exitError{ExitError, fmt.Sprintf("the server's architecture %q is not supported (arm64 or amd64)", arch)}
	}
	bin, err := a.linuxBinary(ctx, o.binary, arch)
	if err != nil {
		return err
	}
	warnings, err := install.PrepareData(ctx, m, data, a.progress)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	ip := pick(sb.PublicIP, sb.PublicIPv6)
	opts := install.Options{Domain: sslipDomain(ip), HTTPSPort: 443, HTTPPort: 80, PublicIP: sb.PublicIP, PublicIPv6: sb.PublicIPv6,
		Server: &platform.ServerConfig{Provider: prov, Name: name, PublicIP: sb.PublicIP, PublicIPv6: sb.PublicIPv6, OwnerIPs: sb.OwnerIPs, RebootWindow: window}}
	res, err := install.Install(ctx, m, bin, opts, a.progress)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	warnings = append(warnings, res.Warnings...)

	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, res.CAPEM, 0o644); err != nil {
		return err
	}
	// A box moved to its own domain (tiffin domain set) stays there: its sslip
	// names stop being served an hour after the switch.
	boxURL := opts.PublicURL()
	if prev := bx.URL; prev != "" && !strings.Contains(prev, opts.Domain) {
		boxURL = prev
	}
	bx.URL, bx.Token, bx.CAFile, bx.Build = boxURL, res.OwnerToken, caFile, res.Build
	f.Current = name
	if err := a.saveBoxes(f); err != nil {
		return err
	}
	a.progress("checking " + bx.URL + " over HTTPS")
	c, err := a.boxClient(bx, bx.Token)
	if err != nil {
		return err
	}
	if err := waitHTTPS(ctx, c, res.Build, 90*time.Second); err != nil {
		return &exitError{ExitError, err.Error()}
	}
	if changed, err := ensureAgentKey(ctx, a, c, bx); err != nil {
		return &exitError{ExitError, err.Error()}
	} else if changed {
		if err := a.saveBoxes(f); err != nil {
			return err
		}
	}
	login, _ := a.loginLink(ctx, c)
	sshCmd := "ssh " + sshArgs(sb)

	out := map[string]any{
		"box": name, "provider": prov, "url": bx.URL, "ip": sb.PublicIP, "ipv6": sb.PublicIPv6, "ssh": sshCmd,
		"build": res.Build[:12], "login": login, "ca": caFile, "mcp": "claude mcp add tiffin -- tiffin mcp",
		"seconds": int(time.Since(start).Seconds()), "rebootWindow": orDefault(window, "off"), "ubuntu": sb.Ubuntu,
	}
	if sshAccess != nil {
		out["sshAccess"] = sshAccess
		out["sshAllowed"] = sshAccess.Summary()
	}
	if plan != nil && plan.Currency != "" {
		out["monthly"] = map[string]any{"net": plan.MonthlyNet, "gross": plan.MonthlyGross, "currency": plan.Currency}
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	if !a.tty() {
		writeJSON(a.io.Out, out)
		return nil
	}
	w := a.io.Out
	fmt.Fprintf(w, "\n%s Your box is up %s\n\n", a.paint("✓", green), a.paint(fmt.Sprintf("(%s · %s · build %s · %s)", name, prov, res.Build[:12], time.Since(start).Round(time.Second)), dim))
	fmt.Fprintf(w, "  %-10s %s\n", "Dashboard", a.paint(bx.URL, bold))
	if login != "" {
		fmt.Fprintf(w, "  %-10s %s %s\n", "Sign in", login, a.paint("(one-time, 10 min)", dim))
	}
	fmt.Fprintf(w, "  %-10s %s\n", "Server", strings.TrimSpace(sb.PublicIP+" "+sb.PublicIPv6))
	fmt.Fprintf(w, "  %-10s %s\n", "SSH", sshCmd)
	if sshAccess != nil {
		fmt.Fprintf(w, "  %-10s %s\n", "", a.paint(sshAccess.Summary(), dim))
	}
	if plan != nil && plan.Currency != "" {
		fmt.Fprintf(w, "  %-10s about %.2f %s a month before VAT (%.2f with VAT)\n", "Cost", plan.MonthlyNet, plan.Currency, plan.MonthlyGross)
	}
	fmt.Fprintf(w, "  %-10s %s\n", "Agents", "claude mcp add tiffin -- tiffin mcp")
	for _, wn := range warnings {
		fmt.Fprintf(w, "  %s %s\n", a.paint("!", amber), wn)
	}
	if !publiclyTrusted(ctx, bx.URL) {
		fmt.Fprintf(w, "\n%s The box's HTTPS certificate comes from its own CA until it has a domain; this CLI trusts it. For browsers: %s\n", a.paint("→", amber), a.paint("tiffin trust", bold))
	}
	return nil
}

// sshArgs is how a person reaches the server by hand.
func sshArgs(sb *serverBox) string {
	t, err := remote.ParseTarget(sb.SSH)
	if err != nil {
		return sb.SSH
	}
	s := ""
	if sb.Identity != "" {
		s += "-i " + sb.Identity + " "
	}
	if t.Port != 22 {
		s += fmt.Sprintf("-p %d ", t.Port)
	}
	return s + t.User + "@" + t.Host
}

// resolveHost turns --host into the server's public addresses.
func resolveHost(ctx context.Context, host string) (string, string, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Is4() {
			return a.String(), "", nil
		}
		return "", a.String(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return "", "", fmt.Errorf("cannot resolve %s; pass --public-ip", host)
	}
	var v4, v6 string
	for _, ip := range ips {
		ip = ip.Unmap()
		if ip.Is4() && v4 == "" {
			v4 = ip.String()
		} else if ip.Is6() && v6 == "" {
			v6 = ip.String()
		}
	}
	return v4, v6, nil
}

func (a *app) printPlan(pl *hetzner.Plan) {
	if !a.tty() {
		writeJSON(a.io.Out, map[string]any{"dryRun": true, "provider": "hetzner", "plan": pl,
			"url": "https://dashboard.<server-ip>" + sslipSuffix})
		return
	}
	w := a.io.Out
	fmt.Fprintf(w, "%s nothing was created. %s\n\n", a.paint("Dry run:", bold), a.paint(fmt.Sprintf("(Hetzner · %s, %s · %s)", pl.Location, pl.City, pl.Arch), dim))
	if len(pl.Create) > 0 {
		fmt.Fprintln(w, "Would create:")
		for _, c := range pl.Create {
			fmt.Fprintf(w, "  %s %s\n", a.paint("+", green), c)
		}
	}
	if len(pl.Reuse) > 0 {
		fmt.Fprintln(w, "Already there, kept:")
		for _, c := range pl.Reuse {
			fmt.Fprintf(w, "  %s %s\n", a.paint("=", dim), c)
		}
	}
	fmt.Fprintf(w, "\nMonthly price from Hetzner (%s, before %s%% VAT):\n", pl.Currency, strings.TrimRight(strings.TrimRight(pl.VATRate, "0"), "."))
	for _, c := range pl.Costs {
		fmt.Fprintf(w, "  %-24s %8.2f\n", c.What, c.MonthlyNet)
	}
	fmt.Fprintf(w, "  %-24s %8.2f  %s\n", a.paint("total", bold), pl.MonthlyNet, a.paint(fmt.Sprintf("(%.2f with VAT; traffic beyond the included amount is extra)", pl.MonthlyGross), dim))
	fmt.Fprintf(w, "\n%s run it again without --dry-run to create the box\n", a.paint("→", amber))
}

func (a *app) printAdoptPlan(ap *hetzner.AdoptPlan) {
	if !a.tty() {
		writeJSON(a.io.Out, map[string]any{"dryRun": true, "provider": "hetzner", "adopt": ap})
		return
	}
	w := a.io.Out
	fmt.Fprintf(w, "%s nothing was changed. Adopting %s %s would:\n", a.paint("Dry run:", bold), ap.Server,
		a.paint(fmt.Sprintf("(id %d · %s in %s · %s · %s)", ap.ID, ap.ServerType, ap.Location, ap.IPv4, ap.Arch), dim))
	for _, c := range ap.Changes {
		fmt.Fprintf(w, "  %s %s\n", a.paint("~", amber), c)
	}
	fmt.Fprintf(w, "\nIt already costs (%s a month, before VAT; adopting adds nothing):\n", ap.Currency)
	for _, c := range ap.Costs {
		fmt.Fprintf(w, "  %-24s %8.2f\n", c.What, c.MonthlyNet)
	}
	fmt.Fprintf(w, "  %-24s %8.2f  %s\n", a.paint("total", bold), ap.MonthlyNet, a.paint(fmt.Sprintf("(%.2f with VAT)", ap.MonthlyGross), dim))
	fmt.Fprintf(w, "\n%s run it again without --dry-run to adopt the server\n", a.paint("→", amber))
}

func (a *app) printSSHPlan(name string, t remote.Target, d install.DataSpec, sb *serverBox) {
	data := install.DataRoot + " on the root disk (database branches copy instead of reflinking unless it is XFS)"
	if d.Device != "" {
		data = d.Device + " (formatted XFS only if blank), mounted at " + install.DataRoot
	} else if d.Dir != "" {
		data = d.Dir + ", bind-mounted at " + install.DataRoot
	}
	url := "https://dashboard." + sslipDomain(pick(sb.PublicIP, sb.PublicIPv6))
	steps := []string{
		"connect to " + t.String() + " and check it runs Ubuntu 24.04 or 26.04 with sudo",
		"data: " + data,
		"install Tiffin and its services; harden the server (security updates, SSH keys only, nftables firewall: 22/80/443, CrowdSec, swap, log caps)",
		"serve the dashboard at " + url,
	}
	if !a.tty() {
		writeJSON(a.io.Out, map[string]any{"dryRun": true, "provider": "ssh", "box": name, "steps": steps, "url": url})
		return
	}
	fmt.Fprintf(a.io.Out, "%s nothing was changed. Tiffin would:\n", a.paint("Dry run:", bold))
	for _, s := range steps {
		fmt.Fprintf(a.io.Out, "  %s %s\n", a.paint("+", green), s)
	}
}

// downServer deletes (hetzner) or detaches (ssh) a server box.
func (a *app) downServer(ctx context.Context, name string, bx *boxConfig, confirmed, deleteData, unprotect bool, tokenFile string) error {
	sb := bx.Server
	if sb == nil {
		sb = &serverBox{}
	}
	confirmLater := func(detail string, extra map[string]any) error {
		hint := "re-run with --confirm " + name
		if a.tty() {
			fmt.Fprintf(a.io.Out, "%s\n%s %s\n", detail, a.paint("→", amber), a.paint("tiffin down --confirm "+name+map[bool]string{true: " --delete-data"}[deleteData], bold))
		} else {
			out := map[string]any{"code": "confirm_required", "status": 428, "detail": detail, "hint": hint}
			for k, v := range extra {
				out[k] = v
			}
			writeJSON(a.io.Out, out)
		}
		a.code = ExitConfirm
		return nil
	}
	forget := func() {
		if f, err := a.loadBoxes(); err == nil {
			delete(f.Boxes, name)
			if f.Current == name {
				f.Current = ""
			}
			_ = a.saveBoxes(f)
		}
		_ = os.RemoveAll(filepath.Join(a.configDir(), "boxes", name))
	}

	if bx.Provider == "ssh" {
		if deleteData {
			return &exitError{ExitInvalid, "Tiffin does not delete data on a server it did not create; after down, remove " + install.DataRoot + " on the server yourself"}
		}
		if !confirmed {
			return confirmLater(fmt.Sprintf("%s this stops Tiffin on %s and forgets the box %s on this computer. The server, its data in %s and the installed packages stay.",
				a.paint("down:", red), sb.SSH, name, install.DataRoot), nil)
		}
		t, err := remote.ParseTarget(sb.SSH)
		var warn string
		if err == nil {
			t.Identity, t.KnownHosts = sb.Identity, sb.KnownHosts
			a.progress("stopping Tiffin on " + t.String())
			if _, stderr, err := remote.New(t).Exec(ctx, "sudo systemctl disable --now tiffin >/dev/null 2>&1; sudo rm -f "+platform.ServerConfigPath); err != nil {
				warn = "could not reach the server to stop Tiffin (" + strings.TrimSpace(stderr) + "); stop it there with: sudo systemctl disable --now tiffin"
			}
		}
		forget()
		if a.tty() {
			fmt.Fprintf(a.io.Out, "%s Tiffin is stopped on %s and %s is forgotten. Its data is still in %s on the server.\n", a.paint("✓", green), sb.SSH, name, install.DataRoot)
			if warn != "" {
				fmt.Fprintf(a.io.Out, "%s %s\n", a.paint("!", amber), warn)
			}
		} else {
			out := map[string]any{"destroyed": name, "provider": "ssh", "kept": []string{install.DataRoot + " on " + sb.SSH}}
			if warn != "" {
				out["warnings"] = []string{warn}
			}
			writeJSON(a.io.Out, out)
		}
		return nil
	}

	// Hetzner.
	hp, err := a.hetznerProvider(name, sb, pick(tokenFile, sb.TokenFile), nil)
	if err != nil {
		return err
	}
	hp.Unprotect = unprotect
	in, err := hp.Inventory(ctx)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	prices, err := hp.Prices(ctx)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	var del, keep []string
	keptNet := 0.0
	for _, s := range in.Servers {
		v4, _ := hetzner.PublicIPs(s)
		del = append(del, fmt.Sprintf("server %s (%s, %s) with its IP addresses", s.Name, s.ServerType.Name, v4))
	}
	for _, x := range in.Firewalls {
		del = append(del, "firewall "+x.Name)
	}
	for _, k := range in.SSHKeys {
		del = append(del, "SSH key "+k.Name)
	}
	for _, ip := range in.PrimaryIPs {
		if ip.AutoDelete || deleteData {
			del = append(del, "IP address "+ip.IP.String())
			continue
		}
		if ip.Type == hcloud.PrimaryIPTypeIPv4 {
			n, g, _ := hetzner.IPv4Price(prices, ip.Location)
			keptNet += n
			keep = append(keep, fmt.Sprintf("IPv4 address %s (DNS may point at it): %.2f %s a month before VAT (%.2f with VAT) while it is unassigned", ip.IP, n, prices.Currency, g))
		} else {
			keep = append(keep, "IP address "+ip.IP.String())
		}
	}
	for _, v := range in.Volumes {
		net, gross := hetzner.VolumePrice(prices, v.Size)
		what := fmt.Sprintf("volume %s (%d GB): every project, database and file on the box", v.Name, v.Size)
		if deleteData {
			del = append(del, what)
		} else {
			keptNet += net
			keep = append(keep, fmt.Sprintf("volume %s (%d GB) with your data: %.2f %s a month before VAT (%.2f with VAT) until you delete it", v.Name, v.Size, net, prices.Currency, gross))
		}
	}
	if !confirmed {
		var b strings.Builder
		if len(del) == 0 && len(keep) == 0 {
			fmt.Fprintf(&b, "Nothing labelled tiffin-box=%s exists in this Hetzner project.", name)
		} else {
			fmt.Fprintf(&b, "%s this deletes, in Hetzner:\n", a.paint("irreversible:", red))
			for _, d := range del {
				fmt.Fprintf(&b, "  - %s\n", d)
			}
			if len(keep) > 0 {
				fmt.Fprintf(&b, "Kept (add --delete-data to delete them too):\n")
			}
			for _, k := range keep {
				fmt.Fprintf(&b, "  = %s\n", k)
			}
			if prot := hetzner.Protected(in, deleteData); len(prot) > 0 && !unprotect {
				fmt.Fprintf(&b, "Protected against deletion (add --unprotect to lift it): %s\n", strings.Join(prot, ", "))
			}
		}
		extra := map[string]any{"delete": del, "keep": keep}
		if prot := hetzner.Protected(in, deleteData); len(prot) > 0 {
			extra["protected"] = prot
		}
		return confirmLater(strings.TrimRight(b.String(), "\n"), extra)
	}
	rep, err := hp.DestroyAll(ctx, deleteData, a.progress)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	// Check: nothing labelled for the box is left but the kept volume.
	left, err := hp.Inventory(ctx)
	if err != nil {
		return &exitError{ExitError, err.Error()}
	}
	keptIPs := 0
	for _, ip := range left.PrimaryIPs {
		if !ip.AutoDelete && !deleteData {
			keptIPs++
		}
	}
	if n := len(left.Servers) + len(left.Firewalls) + len(left.SSHKeys) + len(left.PrimaryIPs) - keptIPs; n > 0 || (deleteData && len(left.Volumes) > 0) {
		return &exitError{ExitError, fmt.Sprintf("Hetzner still lists %d resources labelled tiffin-box=%s; run tiffin down again or delete them in the console", n+len(left.Volumes), name)}
	}
	forget()
	out := map[string]any{"destroyed": name, "provider": "hetzner", "deleted": rep.Deleted}
	if len(keep) > 0 {
		out["kept"] = keep
		out["keptMonthlyNet"] = keptNet
	}
	if !a.tty() {
		writeJSON(a.io.Out, out)
		return nil
	}
	fmt.Fprintf(a.io.Out, "%s Deleted %s.\n", a.paint("✓", green), strings.Join(rep.Deleted, ", "))
	for _, k := range keep {
		fmt.Fprintf(a.io.Out, "%s Kept %s. Delete it with: tiffin down --provider hetzner --confirm %s --delete-data\n", a.paint("!", amber), k, name)
	}
	return nil
}

// publiclyTrusted reports whether url's certificate verifies against this
// computer's own roots, as a browser's would.
func publiclyTrusted(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/health", nil)
	if err != nil {
		return false
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}
