package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/shiptiffin/tiffin/internal/state"
)

// The box installed on this server (see internal/install). Variables so
// tests can point them at a temp dir and act as root.
var (
	serverHome = install.Home
	serverUnit = install.UnitPath
	euid       = os.Geteuid
	rootUID    = uint32(0)
)

// serverBox finds the box installed on this server, for a command given no
// other box: root on the server then needs no --home (`ssh root@<box>
// tiffin login` is how an owner gets back in). "" when this machine has no
// installed box. Only root may use it, and only state that is root's alone
// (owned by root, writable by nobody else), so no one can plant a box that
// root then acts on; a missing state.db is never made.
func (a *app) serverBox() (string, error) {
	if _, err := os.Stat(serverHome); err != nil {
		return "", nil
	}
	if euid() != 0 {
		return "", &exitError{ExitAuth, "this server's Tiffin box belongs to root: run it again with sudo (e.g. sudo tiffin login)"}
	}
	db := filepath.Join(serverHome, "state.db")
	if _, err := os.Stat(db); err != nil {
		return "", nil
	}
	for _, p := range []string{serverHome, db} {
		if err := rootOnly(p); err != nil {
			return "", &exitError{ExitInvalid, fmt.Sprintf("not using the box in %s: %v", serverHome, err)}
		}
	}
	return serverHome, nil
}

// installedURL is the domain and dashboard address of the box installed
// on this server: the options it was installed with (its tiffin.service)
// and the domain set since, chosen as `tiffin serve` chooses them. Never
// the control plane's address in managed.json.
func installedURL(ctx context.Context, db *state.DB) (domain, dashboard string, err error) {
	if err := rootOnly(serverUnit); err != nil {
		return "", "", err
	}
	raw, err := os.ReadFile(serverUnit)
	if err != nil {
		return "", "", err
	}
	o, err := install.OptionsFromUnit(string(raw))
	if err != nil {
		return "", "", err
	}
	var ips []string
	for _, ip := range []string{o.PublicIP, o.PublicIPv6} {
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	_, domain, dashboard, err = boxReach(ctx, db, reachFlags{domain: o.Domain, publicURL: o.PublicURL(), httpsPort: o.HTTPSPort, ips: ips})
	return domain, dashboard, err
}
