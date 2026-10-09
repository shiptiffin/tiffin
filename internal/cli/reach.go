package cli

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/spf13/cobra"
)

// envDefault fills flags the command line left unset from environment
// variables (a systemd drop-in can set them without touching the unit).
func envDefault(cmd *cobra.Command, env func(string) string, flags map[string]string) {
	for flag, name := range flags {
		f := cmd.Flags().Lookup(flag)
		if f == nil || f.Changed {
			continue
		}
		if v := strings.TrimSpace(env(name)); v != "" {
			_ = cmd.Flags().Set(flag, v)
		}
	}
}

type reachFlags struct {
	domain, publicURL string
	httpsPort         int
	tls               string
	ips               []string
	acmeCA, acmeRoots string
	acmeEmail         string
	resolvers         []string
}

// boxReach decides how the world reaches this box: its public addresses,
// its domain (set with tiffin domain set, else <ip>.sslip.io on a server,
// else --domain), a separate apps domain if one is set, and where
// certificates come from. It returns the domain
// and the dashboard URL to serve.
func boxReach(ctx context.Context, db *state.DB, f reachFlags) (platform.Reach, string, string, error) {
	var r platform.Reach
	for _, s := range f.ips {
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			a, err := netip.ParseAddr(part)
			if err != nil {
				return r, "", "", fmt.Errorf("--public-ip %q is not an IP address", part)
			}
			r.PublicIPs = append(r.PublicIPs, a.Unmap())
		}
	}
	if len(r.PublicIPs) == 0 {
		r.PublicIPs = dnskit.LocalPublicIPs()
	}
	set, err := platform.LoadBoxDomain(ctx, db)
	if err != nil {
		return r, "", "", fmt.Errorf("read the box domain: %w", err)
	}
	domain, source, def := platform.ChooseDomain(f.domain, set, r.PublicIPs)
	switch f.tls {
	case "acme":
		r.ACME = true
	case "internal":
	case "auto", "":
		// A public CA can only reach a routable address; a private one (a VM on
		// this machine, a LAN) keeps the internal CA unless a test CA is named.
		r.ACME = (anyRoutable(r.PublicIPs) || strings.TrimSpace(f.acmeCA) != "") && !platform.IsLocalDomain(domain)
	default:
		return r, "", "", fmt.Errorf("--tls %q: want auto, acme or internal", f.tls)
	}
	if !r.ACME && source == "sslip" {
		// sslip.io names only make sense with public certificates.
		domain, source, def = f.domain, "flag", f.domain
	}
	r.DomainSource, r.DefaultDomain, r.Dashboard = source, def, set.Dashboard
	if source == "set" && set.Apps != domain {
		r.AppsDomain = set.Apps
	}
	r.ACMEDirectory, r.ACMERoots = strings.TrimSpace(f.acmeCA), strings.TrimSpace(f.acmeRoots)
	r.ACMEEmail = set.Email
	if r.ACMEEmail == "" {
		r.ACMEEmail = strings.TrimSpace(f.acmeEmail)
	}
	for _, s := range f.resolvers {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				r.Resolvers = append(r.Resolvers, part)
			}
		}
	}
	dash := set.Dashboard
	if dash == "" {
		dash = "dashboard"
	}
	return r, domain, dashboardURL(f.publicURL, dash+"."+domain, f.httpsPort), nil
}

// dashboardURL keeps the public port of the configured URL (a forwarded
// local VM is reached on another port than the box serves on). A plain HTTP
// URL on this machine (a box served without the edge, as the dashboard's
// browser tests do) is used as it is: passkeys must match the page's address.
func dashboardURL(configured, host string, httpsPort int) string {
	port := ""
	u, err := url.Parse(configured)
	if err == nil && u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return strings.TrimRight(configured, "/")
	}
	if err == nil && u.Port() != "" {
		port = u.Port()
	} else if configured == "" && httpsPort != 0 && httpsPort != 443 {
		port = fmt.Sprint(httpsPort)
	}
	if port == "" || port == "443" {
		return "https://" + host
	}
	return "https://" + host + ":" + port
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.IsLoopback()
}

// anyRoutable reports whether any address is reachable from the internet
// (not private, loopback, link-local or shared CGNAT space).
func anyRoutable(ips []netip.Addr) bool {
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	for _, a := range ips {
		if a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a) {
			return true
		}
	}
	return false
}
