// Package install puts Tiffin on a machine and keeps it updated.
//
// Layout on the box:
//
//	/usr/local/lib/tiffin/versions/<sha12>/tiffin   every installed build
//	/usr/local/bin/tiffin -> versions/<sha12>/tiffin the current one (atomic symlink)
//	/etc/systemd/system/tiffin.service             runs `tiffin serve`: the API, dashboard and modules
//	/etc/systemd/system/tiffin-edge.service        runs `tiffin edge`: HTTPS for every site (Caddy, switchboard)
//	/etc/systemd/system/tiffin-edge.socket         holds the edge's ports across its restarts
//	/var/lib/tiffin/platform/                      platform state (XFS data disk)
//
// The edge is its own service so that restarting or updating Tiffin never
// interrupts the apps: tiffin drives it over a socket, and its restarts do
// not restart the edge. Installing and updating are the same operation:
// copy the new binary over, then run `sudo <binary> self-update <binary>`,
// which switches the symlink, restarts tiffin and rolls back if the new
// build is not healthy. The edge picks the new build up when it restarts.
package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/provider"
)

// Box paths and settings.
const (
	VersionsDir = "/usr/local/lib/tiffin/versions"
	BinLink     = "/usr/local/bin/tiffin"
	UnitDir     = "/etc/systemd/system"
	UnitPath    = UnitDir + "/tiffin.service"
	EdgeUnits   = UnitDir + "/tiffin-edge"
	// UnitsBackup holds the unit files as they were before an update wrote
	// new ones, until the new build is healthy: a rollback puts them back.
	UnitsBackup = Home + "/units-before"
	Home        = "/var/lib/tiffin/platform"
	APIAddr     = "127.0.0.1:7070"
	User        = "tiffin"
	KeepBuilds  = 3
)

// Options describe how the box serves.
type Options struct {
	Domain    string // e.g. "tiffin.localhost"
	HTTPSPort int    // the box's HTTPS port, e.g. 8443 locally, 443 on a server
	HTTPPort  int    // e.g. 8080 locally, 80 on a server
	// PublicPort is the port people reach HTTPS on, when it differs from
	// HTTPSPort (a forwarded local VM). 0 means HTTPSPort.
	PublicPort int
	// PublicIP and PublicIPv6 are a real server's public addresses; the
	// platform exposes them to modules (Platform.Reach.PublicIPs).
	PublicIP   string
	PublicIPv6 string
	// Server marks a real server (nil for a local VM): it is written to
	// /etc/tiffin/server.json before provisioning, which hardens the
	// machine and keeps the owner's IP out of CrowdSec bans.
	Server *platform.ServerConfig
}

// PublicURL is the dashboard URL for these options.
func (o Options) PublicURL() string {
	u := "https://dashboard." + o.Domain
	port := o.HTTPSPort
	if o.PublicPort != 0 {
		port = o.PublicPort
	}
	if port != 443 {
		u += fmt.Sprintf(":%d", port)
	}
	return u
}

// Unit renders the systemd unit.
func Unit(o Options) string {
	ips := ""
	if o.PublicIP != "" {
		ips += " --public-ip " + o.PublicIP
	}
	if o.PublicIPv6 != "" {
		ips += " --public-ipv6 " + o.PublicIPv6
	}
	return fmt.Sprintf(`[Unit]
Description=Tiffin box
After=network-online.target local-fs.target tiffin-edge.service
Wants=network-online.target tiffin-edge.service
RequiresMountsFor=/var/lib/tiffin

[Service]
# Root: the box's services (containers, Postgres, Valkey, ...) are managed
# through system tools. The box itself is the isolation boundary; apps run
# in containers.
User=root
ExecStart=%[2]s serve --box --home %[3]s --addr %[4]s --edge-external --domain %[5]s --https-port %[6]d --http-port %[7]d --public-url %[8]s%[9]s
Environment=XDG_DATA_HOME=/var/lib/tiffin/platform/xdg XDG_CONFIG_HOME=/var/lib/tiffin/platform/xdg
Restart=always
RestartSec=2
NoNewPrivileges=yes
ProtectHome=yes
LimitNOFILE=1048576
TimeoutStopSec=30
KillMode=mixed

[Install]
WantedBy=multi-user.target
`, User, BinLink, Home, APIAddr, o.Domain, o.HTTPSPort, o.HTTPPort, o.PublicURL(), ips)
}

// Units are the unit files Install writes, the main one first.
var Units = []string{"tiffin.service", "tiffin-edge.service", "tiffin-edge.socket"}

// EdgeSocketUnit renders the edge's socket unit: systemd holds the HTTPS
// and HTTP ports (and UDP for HTTP/3), so an edge restart refuses no
// connection; they wait for the next edge. ReusePort lets it bind them
// while the tiffin of the old layout still serves on them (see startEdge).
func EdgeSocketUnit(o Options) string {
	return fmt.Sprintf(`[Unit]
Description=Tiffin edge ports (held across edge restarts)

[Socket]
ListenStream=%[1]d
ListenStream=%[2]d
ListenDatagram=%[1]d
Backlog=4096
NoDelay=yes
ReusePort=yes
Service=tiffin-edge.service

[Install]
WantedBy=sockets.target
`, o.HTTPSPort, o.HTTPPort)
}

// EdgeUnit renders the edge's service unit. Restarting tiffin leaves it
// running; restarting it hands its ports over through the socket unit.
func EdgeUnit() string {
	return fmt.Sprintf(`[Unit]
Description=Tiffin edge: HTTPS for every site on the box (Caddy and the switchboard)
Requires=tiffin-edge.socket
After=tiffin-edge.socket network-online.target local-fs.target
RequiresMountsFor=/var/lib/tiffin

[Service]
User=root
ExecStart=%[1]s edge --home %[2]s
Environment=XDG_DATA_HOME=/var/lib/tiffin/platform/xdg XDG_CONFIG_HOME=/var/lib/tiffin/platform/xdg
Restart=always
RestartSec=1
NoNewPrivileges=yes
ProtectHome=yes
LimitNOFILE=1048576
TimeoutStopSec=20
KillMode=mixed

[Install]
WantedBy=multi-user.target
`, BinLink, Home)
}

// Result is what the installer learned from the box.
type Result struct {
	OwnerToken string
	CAPEM      []byte // empty: the box has public certificates
	Build      string
	Warnings   []string // modules that failed to provision (the box still updated)
}

// Install (or update) Tiffin on m from the local linux binary bin.
func Install(ctx context.Context, m provider.Machine, bin string, o Options, progress func(string)) (*Result, error) {
	sum, err := FileSHA(bin)
	if err != nil {
		return nil, err
	}
	progress("copying tiffin " + sum[:12] + " to the box")
	if err := m.Copy(ctx, bin, "/tmp/tiffin.new"); err != nil {
		return nil, err
	}
	progress("installing the service")
	setup := fmt.Sprintf(`set -euo pipefail
id %[1]s >/dev/null 2>&1 || sudo useradd --system --home-dir /var/lib/tiffin --no-create-home --shell /usr/sbin/nologin %[1]s
sudo install -d -m 0700 %[2]s
sudo install -d -m 0755 %[3]s
sudo rm -rf %[9]s && sudo install -d -m 0700 %[9]s
for u in %[10]s; do if [ -e %[11]s/$u ]; then sudo cp %[11]s/$u %[9]s/; fi; done
sudo tee %[4]s >/dev/null <<'UNIT'
%[5]sUNIT
sudo tee %[6]s.socket >/dev/null <<'UNIT'
%[7]sUNIT
sudo tee %[6]s.service >/dev/null <<'UNIT'
%[8]sUNIT
sudo systemctl daemon-reload
sudo systemctl enable tiffin tiffin-edge.socket tiffin-edge.service >/dev/null 2>&1
chmod 0755 /tmp/tiffin.new
`, User, Home, VersionsDir, UnitPath, Unit(o), EdgeUnits, EdgeSocketUnit(o), EdgeUnit(), UnitsBackup, strings.Join(Units, " "), UnitDir)
	if _, stderr, err := m.Exec(ctx, setup); err != nil {
		return nil, fmt.Errorf("set up the service: %w\n%s", err, stderr)
	}
	if o.Server != nil {
		if err := writeServerConfig(ctx, m, o.Server); err != nil {
			return nil, err
		}
	}
	// The installed build (trusted, known to work) performs the update; only
	// the very first install runs the new binary itself.
	progress("provisioning system services (first run installs packages; later runs are quick)")
	if out, stderr, err := m.Exec(ctx, "sudo /tmp/tiffin.new provision"); err != nil {
		return nil, fmt.Errorf("provision: %w\n%s\n%s", err, tail(out, 3000), tail(stderr, 3000))
	}
	var warnings []string
	if raw, _, err := m.Exec(ctx, "sudo cat "+Home+"/provision.json"); err == nil {
		var rep struct {
			Results []struct {
				Module string `json:"module"`
				Error  string `json:"error"`
			} `json:"results"`
		}
		if json.Unmarshal([]byte(raw), &rep) == nil {
			for _, r := range rep.Results {
				if r.Error != "" {
					warnings = append(warnings, r.Module+": "+tail(r.Error, 400))
					progress("warning: " + r.Module + " did not install: " + tail(r.Error, 200))
				}
			}
		}
	}
	progress("starting tiffin (rolls back automatically if unhealthy)")
	// The edge restarts on the new build too (its socket holds the ports
	// meanwhile): otherwise it keeps running the build it started with,
	// and edge changes never reach a box updated with up.
	// The installed build performs the update, unless it predates the
	// service layout being installed (the edge's own units): only the new
	// build knows how to move to it and back. If the update fails, the
	// unit files go back too (the build that did it restored them already,
	// unless it could not run at all).
	script := `set -o pipefail
if [ -x ` + BinLink + ` ] && sudo test -e ` + UnitsBackup + `/tiffin-edge.service; then sudo ` + BinLink + ` self-update --restart-edge /tmp/tiffin.new; else sudo /tmp/tiffin.new self-update --restart-edge /tmp/tiffin.new; fi
rc=$?; rm -f /tmp/tiffin.new
if [ $rc -eq 0 ]; then sudo rm -rf ` + UnitsBackup + `
elif sudo test -e ` + UnitsBackup + `/tiffin.service; then
  for u in ` + strings.Join(Units, " ") + `; do
    if sudo test -e ` + UnitsBackup + `/$u; then sudo cp ` + UnitsBackup + `/$u ` + UnitDir + `/$u; else sudo systemctl disable --now $u >/dev/null 2>&1; sudo rm -f ` + UnitDir + `/$u; fi
  done
  sudo systemctl daemon-reload
fi
exit $rc`
	if out, stderr, err := m.Exec(ctx, script); err != nil {
		var p struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal([]byte(out), &p) == nil && p.Detail != "" {
			out = p.Detail
		}
		return nil, fmt.Errorf("%s\n%s", strings.TrimSpace(out), strings.TrimSpace(stderr))
	}
	tok, stderr, err := m.Exec(ctx, "sudo cat "+Home+"/owner-token")
	if err != nil {
		return nil, fmt.Errorf("read owner token: %w\n%s", err, stderr)
	}
	ca, stderr, err := m.Exec(ctx, caScript(Home))
	if err != nil {
		return nil, fmt.Errorf("read the box's CA certificate: %w\n%s", err, stderr)
	}
	return &Result{OwnerToken: strings.TrimSpace(tok), CAPEM: []byte(ca), Build: sum, Warnings: warnings}, nil
}

// TLSModeFile, in the box's home, says where its certificates come from:
// "acme" (a public CA: clients need nothing) or "internal" (the box's own
// CA, which clients must trust). The service writes it before it answers.
const TLSModeFile = "tls-mode"

// caScript prints the box's CA certificate, or nothing when the box has
// public certificates. The API answers health before the HTTPS edge has
// written its CA (a fresh box makes the CA on first start): it waits for it.
func caScript(home string) string {
	return `if [ "$(sudo cat ` + home + `/` + TLSModeFile + ` 2>/dev/null)" = acme ]; then exit 0; fi
for i in $(seq 1 60); do sudo test -s ` + home + `/ca.crt && break; sleep 0.5; done
sudo cat ` + home + `/ca.crt`
}

// writeServerConfig records on the box that it runs on a real server.
func writeServerConfig(ctx context.Context, m provider.Machine, c *platform.ServerConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	script := fmt.Sprintf("set -euo pipefail\nsudo install -d -m 0755 %[1]s\nsudo tee %[2]s.tmp >/dev/null <<'JSON'\n%[3]s\nJSON\nsudo mv %[2]s.tmp %[2]s\n",
		path.Dir(platform.ServerConfigPath), platform.ServerConfigPath, raw)
	if _, stderr, err := m.Exec(ctx, script); err != nil {
		return fmt.Errorf("write %s: %w\n%s", platform.ServerConfigPath, err, stderr)
	}
	return nil
}

// FileSHA returns the hex SHA-256 of a file.
func FileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
