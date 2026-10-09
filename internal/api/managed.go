package api

import (
	"net/url"
	"strings"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// Managed is how a box that ShipTiffin installed links back to the
// customer's ShipTiffin account. A box installed with `tiffin up` has none.
type Managed struct {
	Account string `json:"account" doc:"The ShipTiffin account page, where the owner manages the plan, billing and subscription"`
	Paused  string `json:"paused,omitempty" doc:"Why automatic updates are paused (the subscription is not active); empty when they are not"`
}

// managedInfo reads the box's managed config: nil when the box is not
// managed, or its control plane address is not one to link to.
func managedInfo() *Managed {
	c, err := platform.LoadManagedConfig()
	if err != nil || c == nil {
		return nil
	}
	account := accountURL(c.ControlPlane)
	if account == "" {
		return nil
	}
	m := &Managed{Account: account}
	if paused, why := platform.ManagedUpdatesPaused(); paused {
		m.Paused = why
	}
	return m
}

// accountURL is the control plane's /account page: https://shiptiffin.com
// gives https://shiptiffin.com/account. Anything but an https address (or
// http on loopback, for tests) gives "".
func accountURL(controlPlane string) string {
	u, err := url.Parse(controlPlane)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost")) {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimRight(u.Path, "/") + "/account"}).String()
}
