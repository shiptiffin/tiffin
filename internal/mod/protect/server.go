package protect

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// On a real server (/etc/tiffin/server.json exists) CrowdSec also guards
// SSH: it reads sshd's journal, and a firewall bouncer turns its bans into
// nftables drops, so a banned IP cannot reach port 22 (or anything else).
// The owner can never be locked out: the IPs `tiffin up` and the owner's CLI
// connect from are on a CrowdSec allowlist, which stops decisions for them
// from being made at all.
const (
	fwBouncerPkg     = "crowdsec-firewall-bouncer-nftables=0.0.36" // pinned; apt verifies the repo signature
	fwBouncerName    = "tiffin-firewall"
	fwBouncerUnit    = "crowdsec-firewall-bouncer"
	fwBouncerConfig  = "/etc/crowdsec/bouncers/crowdsec-firewall-bouncer.yaml.local"
	sshAcquis        = "/etc/crowdsec/acquis.d/tiffin-sshd.yaml"
	ownerAllowlist   = "tiffin-owner"
	ownerSeenExpiry  = 30 * 24 * time.Hour // an address the CLI used stays allowed for 30 days after its last use
	ownerSeenRefresh = 6 * time.Hour
)

const sshAcquisYAML = `# Managed by tiffin provision: sshd's log, for SSH brute-force detection.
source: journalctl
journalctl_filter:
  - "_SYSTEMD_UNIT=ssh.service"
labels:
  type: syslog
`

func fwBouncerYAML(key string) string {
	return `# Managed by tiffin provision: talk to the box's own CrowdSec API.
api_url: http://` + lapiAddr + `/
api_key: ` + key + `
mode: nftables
`
}

// sshAcquired reports whether some acquisition already reads sshd's log
// (the package's own setup adds /var/log/auth.log when it finds it).
func sshAcquired() bool {
	files, _ := filepath.Glob("/etc/crowdsec/acquis.d/*.yaml")
	files = append(files, "/etc/crowdsec/acquis.yaml")
	for _, f := range files {
		if f == sshAcquis {
			continue
		}
		raw, err := os.ReadFile(f)
		if err == nil && (strings.Contains(string(raw), "/var/log/auth.log") || strings.Contains(string(raw), "ssh.service")) {
			return true
		}
	}
	return false
}

func provisionServerProtection(ctx context.Context, s *platform.System, c *platform.ServerConfig) error {
	changed := false
	if sshAcquired() {
		if _, err := os.Stat(sshAcquis); err == nil {
			_ = os.Remove(sshAcquis)
			changed = true
		}
	} else {
		c, err := s.WriteFile(sshAcquis, []byte(sshAcquisYAML), 0o644)
		if err != nil {
			return err
		}
		changed = c
	}
	if changed {
		s.Log("crowdsec: watching sshd")
		if _, err := s.Run(ctx, "systemctl", "restart", "crowdsec"); err != nil {
			return err
		}
		if err := s.WaitTCP(ctx, lapiAddr, 60*time.Second); err != nil {
			return err
		}
	}

	// The owner first, before anything can be banned.
	if err := ensureAllowlist(ctx, s); err != nil {
		return err
	}
	for _, ip := range c.OwnerIPs {
		if err := allowOwner(ctx, ip, 0); err != nil {
			return fmt.Errorf("allowlist %s: %w", ip, err)
		}
	}

	// The firewall bouncer: bans become nftables drops.
	if err := s.Apt(ctx, fwBouncerPkg); err != nil {
		return err
	}
	out, err := s.Run(ctx, cscli, "bouncers", "list", "-o", "json")
	if err != nil {
		return err
	}
	key := ""
	if raw, err := os.ReadFile(fwBouncerConfig); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, "api_key: "); ok {
				key = strings.TrimSpace(v)
			}
		}
	}
	if !strings.Contains(out, `"`+fwBouncerName+`"`) || len(key) < 32 {
		s.Log("crowdsec: registering the firewall bouncer")
		_, _ = s.Run(ctx, cscli, "bouncers", "delete", fwBouncerName)
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		key = hex.EncodeToString(b)
		if _, err := s.Run(ctx, cscli, "bouncers", "add", fwBouncerName, "-k", key); err != nil {
			return err
		}
	}
	cfgChanged, err := s.WriteFile(fwBouncerConfig, []byte(fwBouncerYAML(key)), 0o600)
	if err != nil {
		return err
	}
	active := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", fwBouncerUnit).Run() == nil
	if cfgChanged || !active {
		if _, err := s.Run(ctx, "systemctl", "enable", fwBouncerUnit); err != nil {
			return err
		}
		if _, err := s.Run(ctx, "systemctl", "restart", fwBouncerUnit); err != nil {
			return err
		}
	}
	return nil
}

func ensureAllowlist(ctx context.Context, s *platform.System) error {
	out, err := s.Run(ctx, cscli, "allowlists", "list", "-o", "json")
	if err != nil {
		return err
	}
	if strings.Contains(out, `"`+ownerAllowlist+`"`) {
		return nil
	}
	_, err = s.Run(ctx, cscli, "allowlists", "create", ownerAllowlist, "-d", "Addresses the box's owner connects from (tiffin up and the CLI); never banned")
	return err
}

// allowOwner puts an address (or IPv6 /64) on the owner allowlist and lifts
// any ban on it. expiry 0 means until removed.
func allowOwner(ctx context.Context, target string, expiry time.Duration) error {
	pf, err := platform.ParseIPOrPrefix(target)
	if err != nil {
		return err
	}
	value := pf.String()
	if pf.IsSingleIP() {
		value = pf.Addr().String()
	}
	args := []string{"allowlists", "add", ownerAllowlist, value, "-d", "owner (" + time.Now().UTC().Format("2006-01-02") + ")"}
	if expiry > 0 {
		args = append(args, "-e", expiry.String())
	}
	if out, err := exec.CommandContext(ctx, cscli, args...).CombinedOutput(); err != nil && !strings.Contains(string(out), "already") {
		return fmt.Errorf("cscli %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	t, _ := targetArgs(value)
	_ = exec.CommandContext(ctx, cscli, append([]string{"decisions", "delete"}, t...)...).Run()
	return nil
}

// OwnerSeen keeps the address the owner's CLI just authenticated from out of
// CrowdSec bans (real servers only). Cheap: at most one cscli call per
// address every few hours, in the background.
func (m *Module) OwnerSeen(_ context.Context, p *platform.Platform, ip string) {
	if p == nil || len(p.Reach.PublicIPs) == 0 {
		return // a local box: everything arrives over loopback
	}
	pf, err := platform.ParseIPOrPrefix(ip)
	if err != nil || !public(pf.Addr()) {
		return
	}
	key := pf.String()
	m.ownerMu.Lock()
	if m.ownerSeen == nil {
		m.ownerSeen = map[string]time.Time{}
	}
	now := m.clock()
	if t, ok := m.ownerSeen[key]; ok && now.Sub(t) < ownerSeenRefresh {
		m.ownerMu.Unlock()
		return
	}
	m.ownerSeen[key] = now
	allow := m.allow
	m.ownerMu.Unlock()
	if allow == nil {
		allow = func(ctx context.Context, target string) error {
			if _, err := os.Stat(cscli); err != nil {
				return nil
			}
			return allowOwner(ctx, target, ownerSeenExpiry)
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := allow(ctx, key); err != nil && p.Log != nil {
			p.Log.Warn("owner allowlist", "ip", key, "err", err)
		}
	}()
}

func public(a netip.Addr) bool {
	return a.IsValid() && !a.IsLoopback() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !a.IsUnspecified()
}

// ServerProtection is the SSH side of CrowdSec on a real server.
type ServerProtection struct {
	Bouncer  bool     `json:"bouncer" doc:"The firewall bouncer runs: bans drop all traffic from the IP, SSH included."`
	SSH      bool     `json:"ssh" doc:"CrowdSec reads sshd's log."`
	OwnerIPs []string `json:"ownerIPs" doc:"Addresses on the owner allowlist: never banned."`
}

func serverProtection(ctx context.Context) ServerProtection {
	var sp ServerProtection
	sp.Bouncer = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", fwBouncerUnit).Run() == nil
	sp.SSH = sshAcquired() || fileExistsP(sshAcquis)
	if out, err := run(ctx, "allowlists", "inspect", ownerAllowlist, "-o", "raw"); err == nil {
		sp.OwnerIPs = allowlistValues(string(out))
	}
	return sp
}

// allowlistValues reads the addresses out of `cscli allowlists inspect -o
// raw` (CSV: name,description,value,comment,expiration,...).
func allowlistValues(raw string) []string {
	r := csv.NewReader(strings.NewReader(raw))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil || len(rows) == 0 {
		return nil
	}
	col := slices.Index(rows[0], "value")
	if col < 0 {
		return nil
	}
	var out []string
	for _, row := range rows[1:] {
		if col < len(row) {
			if _, err := platform.ParseIPOrPrefix(strings.TrimSpace(row[col])); err == nil {
				out = append(out, strings.TrimSpace(row[col]))
			}
		}
	}
	return out
}

func fileExistsP(p string) bool { _, err := os.Stat(p); return err == nil }
