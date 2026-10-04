package base

import (
	"strings"
	"testing"
)

func TestSwapSize(t *testing.T) {
	const gib = 1 << 30
	for ram, want := range map[uint64]uint64{1 * gib: 1 * gib, 4 * gib: 2 * gib, 8 * gib: 4 * gib, 64 * gib: 4 * gib} {
		if got := swapSize(ram); got != want {
			t.Errorf("RAM %d GiB: swap %d GiB, want %d", ram/gib, got/gib, want/gib)
		}
	}
}

func TestUnattendedConfig(t *testing.T) {
	if c := unattendedConfig(""); !strings.Contains(c, `Automatic-Reboot "false"`) || strings.Contains(c, "Reboot-Time") {
		t.Fatalf("off by default:\n%s", c)
	}
	if c := unattendedConfig("04:30"); !strings.Contains(c, `Automatic-Reboot "true"`) || !strings.Contains(c, `Automatic-Reboot-Time "04:30"`) {
		t.Fatalf("window:\n%s", c)
	}
	if !strings.Contains(sshdConfig, "PasswordAuthentication no") || !strings.Contains(sshdConfig, "PermitRootLogin prohibit-password") {
		t.Fatal("sshd config must turn off passwords")
	}
}
