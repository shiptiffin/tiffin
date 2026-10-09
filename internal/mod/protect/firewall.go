package protect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// The baseline firewall: one nftables table, loaded by a oneshot unit at
// boot and replaced atomically by every `tiffin provision`.
//
// Only the public interfaces (the ones carrying a default route, plus any
// listed in firewallIfacesFile) are filtered: loopback, container bridges
// and private links stay open, so the box's own services and Lima's port
// forwarding (which arrives on loopback) keep working. On a public
// interface, inbound is dropped except SSH, the edge ports, established
// traffic, DHCP and rate-limited ICMP.
//
// A way back: `sudo touch /etc/tiffin/firewall.off && sudo tiffin provision`
// (or `sudo systemctl stop tiffin-firewall`) removes the table.
const (
	firewallTable      = "tiffin_guard"
	firewallFile       = "/etc/tiffin/firewall.nft"
	firewallUnit       = "tiffin-firewall.service"
	firewallOffFile    = "/etc/tiffin/firewall.off"
	firewallIfacesFile = "/etc/tiffin/firewall-interfaces"
	tiffinUnitFile     = "/etc/systemd/system/tiffin.service"
)

// firewallPlan is what the ruleset is rendered from.
type firewallPlan struct {
	Interfaces []string // public interfaces to filter
	EdgePorts  []int    // TCP ports the edge listens on
}

var ifaceRE = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)

// renderFirewall returns the nft script. It is pure, for tests.
func renderFirewall(p firewallPlan) (string, error) {
	if len(p.Interfaces) == 0 {
		return "", fmt.Errorf("no public interface found to filter")
	}
	for _, i := range p.Interfaces {
		if !ifaceRE.MatchString(i) {
			return "", fmt.Errorf("unexpected interface name %q", i)
		}
	}
	ifaces := `"` + strings.Join(p.Interfaces, `", "`) + `"`
	ports := make([]string, len(p.EdgePorts))
	for i, port := range p.EdgePorts {
		ports[i] = strconv.Itoa(port)
	}
	edge := strings.Join(ports, ", ")
	return fmt.Sprintf(`#!/usr/sbin/nft -f
# Tiffin baseline firewall. Managed by tiffin provision; edits are overwritten.
# Turn it off: sudo touch %[3]s && sudo tiffin provision
table inet %[4]s
delete table inet %[4]s

table inet %[4]s {
	# New connections per source IP (SSH) and live connections per source IP (edge).
	set ssh4 { type ipv4_addr; flags dynamic, timeout; timeout 2m; size 65536; }
	set ssh6 { type ipv6_addr; flags dynamic, timeout; timeout 2m; size 65536; }
	set syn4 { type ipv4_addr; flags dynamic, timeout; timeout 1m; size 65536; }
	set syn6 { type ipv6_addr; flags dynamic, timeout; timeout 1m; size 65536; }
	set conn4 { type ipv4_addr; flags dynamic; size 65536; }
	set conn6 { type ipv6_addr; flags dynamic; size 65536; }

	chain input {
		type filter hook input priority filter; policy drop;

		# Only public interfaces are filtered.
		iifname != { %[1]s } accept

		ct state invalid drop
		ct state established,related accept

		# ICMP: errors and neighbour discovery always, pings rate limited.
		icmp type echo-request limit rate 20/second burst 50 packets accept
		icmp type echo-request drop
		meta l4proto icmp accept
		icmpv6 type echo-request limit rate 20/second burst 50 packets accept
		icmpv6 type echo-request drop
		meta l4proto ipv6-icmp accept

		# DHCP replies.
		udp dport { 68, 546 } accept

		# SSH: private networks freely; elsewhere at most 15 new connections a minute per IP.
		tcp dport 22 ip saddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16 } accept
		tcp dport 22 ip6 saddr { fc00::/7, fe80::/10 } accept
		tcp dport 22 ct state new add @ssh4 { ip saddr limit rate over 15/minute burst 15 packets } drop
		tcp dport 22 ct state new add @ssh6 { ip6 saddr limit rate over 15/minute burst 15 packets } drop
		tcp dport 22 accept

		# The edge: at most 256 open connections and 100 new ones a second per IP.
		tcp dport { %[2]s } ct state new add @conn4 { ip saddr ct count over 256 } reject with tcp reset
		tcp dport { %[2]s } ct state new add @conn6 { ip6 saddr ct count over 256 } reject with tcp reset
		tcp dport { %[2]s } ct state new add @syn4 { ip saddr limit rate over 100/second burst 200 packets } drop
		tcp dport { %[2]s } ct state new add @syn6 { ip6 saddr limit rate over 100/second burst 200 packets } drop
		tcp dport { %[2]s } accept

		# HTTP/3 (QUIC) on a public box.
		udp dport 443 accept

		# Everything else on a public interface is dropped (policy).
		counter comment "dropped inbound"
	}
}
`, ifaces, edge, firewallOffFile, firewallTable), nil
}

const firewallUnitBody = `[Unit]
Description=Tiffin baseline firewall (nftables)
Documentation=file://` + firewallFile + `
DefaultDependencies=no
Before=network-pre.target
Wants=network-pre.target
After=local-fs.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f ` + firewallFile + `
ExecReload=/usr/sbin/nft -f ` + firewallFile + `
ExecStop=-/usr/sbin/nft delete table inet ` + firewallTable + `

[Install]
WantedBy=multi-user.target
`

// publicInterfaces are the interfaces carrying a default route (v4 or v6),
// plus any listed in firewallIfacesFile.
func publicInterfaces(ctx context.Context) []string {
	var out []string
	for _, fam := range []string{"-4", "-6"} {
		raw, err := exec.CommandContext(ctx, "ip", "-j", fam, "route", "show", "default").Output()
		if err != nil {
			continue
		}
		var routes []struct {
			Dev string `json:"dev"`
		}
		if json.Unmarshal(raw, &routes) == nil {
			for _, r := range routes {
				if r.Dev != "" && r.Dev != "lo" && !slices.Contains(out, r.Dev) {
					out = append(out, r.Dev)
				}
			}
		}
	}
	if raw, err := os.ReadFile(firewallIfacesFile); err == nil {
		for _, f := range strings.Fields(string(raw)) {
			if !strings.HasPrefix(f, "#") && !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

var portFlagRE = regexp.MustCompile(`--(https?)-port[= ](\d+)`)

// edgePorts are 80 and 443 plus whatever the tiffin unit serves on
// (8080/8443 on a local box).
func edgePorts() []int {
	ports := []int{80, 443}
	if raw, err := os.ReadFile(tiffinUnitFile); err == nil {
		for _, m := range portFlagRE.FindAllStringSubmatch(string(raw), -1) {
			if p, err := strconv.Atoi(m[2]); err == nil && p > 0 && p < 65536 && !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
		}
	}
	sort.Ints(ports)
	return ports
}

func provisionFirewall(ctx context.Context, s *platform.System) error {
	if _, err := os.Stat(firewallOffFile); err == nil {
		if _, err := os.Stat("/etc/systemd/system/" + firewallUnit); err == nil {
			s.Log("firewall turned off (" + firewallOffFile + " exists)")
			_, _ = s.Run(ctx, "systemctl", "disable", "--now", firewallUnit)
		}
		_, _ = s.Run(ctx, "nft", "delete", "table", "inet", firewallTable)
		return nil
	}
	if err := s.Apt(ctx, "nftables"); err != nil {
		return err
	}
	script, err := renderFirewall(firewallPlan{Interfaces: publicInterfaces(ctx), EdgePorts: edgePorts()})
	if err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	// Check the rules before they replace anything.
	tmp := firewallFile + ".new"
	if _, err := s.WriteFile(tmp, []byte(script), 0o644); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "nft", "--check", "-f", tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("firewall rules rejected: %w", err)
	}
	os.Remove(tmp)
	changed, err := s.WriteFile(firewallFile, []byte(script), 0o644)
	if err != nil {
		return err
	}
	if _, err := s.WriteFile("/etc/sysctl.d/91-tiffin-protect.conf", []byte(
		"net.ipv4.tcp_syncookies=1\nnet.ipv4.tcp_max_syn_backlog=4096\nnet.ipv4.icmp_echo_ignore_broadcasts=1\n"+
			"net.core.rmem_max=7500000\nnet.core.wmem_max=7500000\n"), 0o644); err != nil { // UDP buffers QUIC asks for
		return err
	}
	if _, err := s.Run(ctx, "sysctl", "--load=/etc/sysctl.d/91-tiffin-protect.conf"); err != nil {
		return err
	}
	if err := s.Unit(ctx, firewallUnit, firewallUnitBody); err != nil {
		return err
	}
	if changed {
		s.Log("firewall rules updated")
		_, err = s.Run(ctx, "systemctl", "reload", firewallUnit)
	}
	return err
}

// FirewallState is the baseline firewall's state.
type FirewallState struct {
	Installed  bool     `json:"installed"`
	Active     bool     `json:"active"`
	Off        bool     `json:"off" doc:"Turned off on purpose (/etc/tiffin/firewall.off exists)."`
	Interfaces []string `json:"interfaces,omitempty" doc:"Public interfaces being filtered."`
	OpenPorts  []int    `json:"openPorts,omitempty" doc:"TCP ports open on them (SSH and the edge)."`
	Detail     string   `json:"detail"`
}

var (
	ifacesLineRE = regexp.MustCompile(`iifname != \{ ([^}]*) \} accept`)
	edgeLineRE   = regexp.MustCompile(`tcp dport \{ ([0-9, ]+) \} accept`)
)

func firewallState(ctx context.Context) FirewallState {
	var st FirewallState
	if _, err := os.Stat(firewallOffFile); err == nil {
		st.Off = true
		st.Detail = "turned off on purpose (" + firewallOffFile + " exists); remove it and run `sudo tiffin provision` to turn it back on"
	}
	raw, err := os.ReadFile(firewallFile)
	if err != nil || st.Off {
		st.Installed = err == nil
		return st
	}
	st.Installed = true
	if m := ifacesLineRE.FindSubmatch(raw); m != nil {
		for _, f := range strings.Split(string(m[1]), ",") {
			st.Interfaces = append(st.Interfaces, strings.Trim(strings.TrimSpace(f), `"`))
		}
	}
	st.OpenPorts = []int{22}
	if m := edgeLineRE.FindSubmatch(raw); m != nil {
		for _, f := range strings.Split(string(m[1]), ",") {
			if p, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
				st.OpenPorts = append(st.OpenPorts, p)
			}
		}
	}
	st.Active = exec.CommandContext(ctx, "nft", "list", "table", "inet", firewallTable).Run() == nil
	if st.Active {
		st.Detail = fmt.Sprintf("filtering %s: open ports %v, everything else dropped", strings.Join(st.Interfaces, ", "), st.OpenPorts)
	} else {
		st.Detail = "rules are installed but not loaded; run `sudo systemctl restart " + firewallUnit + "`"
	}
	return st
}
