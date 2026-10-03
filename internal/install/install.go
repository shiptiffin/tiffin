// Package install puts Tiffin on a machine and keeps it updated.
//
// Layout on the box:
//
//	/usr/local/lib/tiffin/versions/<sha12>/tiffin   every installed build
//	/usr/local/bin/tiffin -> versions/<sha12>/tiffin the current one (atomic symlink)
//	/etc/systemd/system/tiffin.service             runs `tiffin serve` as user tiffin
//	/var/lib/tiffin/platform/                      platform state (XFS data disk)
//
// Installing and updating are the same operation: copy the new binary over,
// then run `sudo <binary> self-update <binary>`, which switches the symlink,
// restarts the service and rolls back if the new build is not healthy.
package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/btahir/tiffin/internal/provider"
)

// Box paths and settings.
const (
	VersionsDir = "/usr/local/lib/tiffin/versions"
	BinLink     = "/usr/local/bin/tiffin"
	UnitPath    = "/etc/systemd/system/tiffin.service"
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
	return fmt.Sprintf(`[Unit]
Description=Tiffin box
After=network-online.target local-fs.target
Wants=network-online.target
RequiresMountsFor=/var/lib/tiffin

[Service]
# Root: the box's services (containers, Postgres, Valkey, ...) are managed
# through system tools. The box itself is the isolation boundary; apps run
# in containers.
User=root
ExecStart=%[2]s serve --box --home %[3]s --addr %[4]s --edge --domain %[5]s --https-port %[6]d --http-port %[7]d --public-url %[8]s
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
`, User, BinLink, Home, APIAddr, o.Domain, o.HTTPSPort, o.HTTPPort, o.PublicURL())
}

// Result is what the installer learned from the box.
type Result struct {
	OwnerToken string
	CAPEM      []byte
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
sudo tee %[4]s >/dev/null <<'UNIT'
%[5]sUNIT
sudo systemctl daemon-reload
sudo systemctl enable tiffin >/dev/null 2>&1
chmod 0755 /tmp/tiffin.new
`, User, Home, VersionsDir, UnitPath, Unit(o))
	if _, stderr, err := m.Exec(ctx, setup); err != nil {
		return nil, fmt.Errorf("set up the service: %w\n%s", err, stderr)
	}
	progress("starting tiffin (rolls back automatically if unhealthy)")
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
	script := `set -o pipefail
if [ -x ` + BinLink + ` ]; then sudo ` + BinLink + ` self-update /tmp/tiffin.new; else sudo /tmp/tiffin.new self-update /tmp/tiffin.new; fi
rc=$?; rm -f /tmp/tiffin.new; exit $rc`
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
	ca, stderr, err := m.Exec(ctx, "sudo cat "+Home+"/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read the box's CA certificate: %w\n%s", err, stderr)
	}
	return &Result{OwnerToken: strings.TrimSpace(tok), CAPEM: []byte(ca), Build: sum, Warnings: warnings}, nil
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
