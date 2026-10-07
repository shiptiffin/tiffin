package runtime

import (
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/mod/budget"
)

// The box firewall keeps app ports local and refuses apps' own connections
// to port 25 elsewhere: their mail goes through the box (SMTP_URL).
func TestFirewallRules(t *testing.T) {
	for _, want := range []string{
		`iifname != "lo" tcp dport 20000-29999 drop`,
		`type filter hook output priority filter; policy accept;`,
		`oifname != "lo" tcp dport 25 socket cgroupv2 level 2 "tiffin.slice/tiffin-p.slice" reject with tcp reset`,
	} {
		if !strings.Contains(firewallRules, want) {
			t.Errorf("rules lack %q:\n%s", want, firewallRules)
		}
	}
	// The app cgroup is where budget puts every project's slice.
	if dir := budget.ParentDir(""); dir != "/sys/fs/cgroup/"+appsCgroup {
		t.Fatalf("apps run in %s, the rule matches %s", dir, appsCgroup)
	}
	// nft resolves the cgroup at load: the slice must exist first.
	if !strings.Contains(firewallUnit, "Wants=tiffin-p.slice") || !strings.Contains(firewallUnit, "After=network.target tiffin-p.slice") {
		t.Fatalf("unit:\n%s", firewallUnit)
	}
}
