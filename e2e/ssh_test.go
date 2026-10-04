//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The SSH-provider acceptance test installs a box onto a fresh Lima VM that
// stands in for a plain Ubuntu server: no Tiffin template, no port forwards,
// reached only over SSH and its own network address, with a blank extra disk
// like a cloud volume. It proves the real-server path end to end:
//
//	tiffin up --provider ssh → HTTPS on dashboard.<ip>.sslip.io (the CLI
//	pins the box's CA) → hardening checks on the machine → deploy a starter
//	(Postgres + an app) → reboot (data disk, services and app come back) →
//	update with a new build → tiffin down.
//
// TIFFIN_E2E_UBUNTU=26.04 runs it on Ubuntu 26.04 instead of 24.04.
// TIFFIN_E2E_SSH_INSTANCE / _DISK override the VM and disk names.
const (
	sshInstanceDefault = "dev-ssh"
	sshDiskDefault     = "dssh"
)

var ubuntuImages = map[string]map[string]string{
	"24.04": {
		"aarch64": "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-arm64.img",
		"x86_64":  "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-amd64.img",
	},
	"26.04": {
		"aarch64": "https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-arm64.img",
		"x86_64":  "https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-amd64.img",
	},
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func TestSSHProvider(t *testing.T) {
	RequireLima(t)
	if runtime.GOOS != "darwin" {
		t.Skip("the SSH-provider e2e needs vzNAT (macOS)")
	}
	release := envOr("TIFFIN_E2E_UBUNTU", "24.04")
	imgs, ok := ubuntuImages[release]
	if !ok {
		t.Fatalf("TIFFIN_E2E_UBUNTU=%s: use 24.04 or 26.04", release)
	}
	instance, disk := envOr("TIFFIN_E2E_SSH_INSTANCE", sshInstanceDefault), envOr("TIFFIN_E2E_SSH_DISK", sshDiskDefault)
	start := time.Now()
	phase := phaseLogger(t)
	dir := t.TempDir()

	lima := func(timeout time.Duration, args ...string) string {
		t.Helper()
		out, stderr, err := runCmd(timeout, "limactl", args...)
		if err != nil {
			t.Fatalf("limactl %s: %v\n%s\n%s", strings.Join(args, " "), err, tail(out, 2000), tail(stderr, 3000))
		}
		return strings.TrimSpace(out)
	}
	destroyVM := func() {
		_, _, _ = runCmd(3*time.Minute, "limactl", "delete", "-f", "--tty=false", instance)
		_, _, _ = runCmd(time.Minute, "limactl", "disk", "delete", "-f", "--tty=false", disk)
	}
	destroyVM() // a leftover from an interrupted run
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
			destroyVM()
		}
	})

	// ---- a plain Ubuntu server ----
	p := time.Now()
	lima(time.Minute, "disk", "create", disk, "--size", "10GiB", "--format", "raw", "--tty=false")
	set := fmt.Sprintf(`.images = [{"location": %q, "arch": "aarch64"}, {"location": %q, "arch": "x86_64"}] | .additionalDisks = [{"name": %q, "format": false}]`,
		imgs["aarch64"], imgs["x86_64"], disk)
	lima(25*time.Minute, "create", "--name", instance, "--tty=false", "--set", set, filepath.Join(RepoRoot(), "e2e", "lima", "plain-ubuntu.yaml"))
	lima(15*time.Minute, "start", instance, "--tty=false", "--timeout", "14m")
	shell := func(script string) (string, error) {
		out, stderr, err := runCmd(5*time.Minute, "limactl", "shell", "--workdir", "/", instance, "--", "bash", "-c", script)
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, stderr)
		}
		return strings.TrimSpace(out), nil
	}
	mustShell := func(script string) string {
		t.Helper()
		out, err := shell(script)
		if err != nil {
			t.Fatalf("on the server %q: %v\n%s", script, err, out)
		}
		return out
	}
	ip := mustShell(`ip -4 -o addr show lima0 | awk '{print $4}' | cut -d/ -f1`)
	if net.ParseIP(ip) == nil {
		t.Fatalf("no vzNAT address: %q", ip)
	}
	osr := mustShell(`. /etc/os-release; echo $VERSION_ID`)
	user := mustShell(`whoami`)
	dev := mustShell(`lsblk -dnpo NAME,TYPE,SIZE | awk '$2=="disk" && $3=="10G"{print $1}' | head -n1`)
	if osr != release || dev == "" {
		t.Fatalf("server: ubuntu %q, blank disk %q", osr, dev)
	}
	if os.Getenv("TIFFIN_E2E_PREMOUNT") == "1" {
		// Like a Hetzner volume made in the console: XFS without a label,
		// automounted at /mnt/HC_Volume_<id> from fstab. up must move it.
		mustShell(`set -e; sudo mkfs.xfs -q ` + dev + `; sudo mkdir -p /mnt/HC_Volume_123
echo "` + dev + ` /mnt/HC_Volume_123 xfs discard,nofail,defaults 0 0" | sudo tee -a /etc/fstab >/dev/null
sudo systemctl daemon-reload; sudo mount /mnt/HC_Volume_123`)
		t.Logf("pre-mounted %s at /mnt/HC_Volume_123 (XFS, no label), like a Hetzner volume", dev)
	}
	home, _ := os.UserHomeDir()
	key := filepath.Join(home, ".lima", "_config", "user")
	t.Logf("server: Ubuntu %s at %s, user %s, blank disk %s", osr, ip, user, dev)
	phase("server", p)

	// ---- tiffin up --provider ssh ----
	p = time.Now()
	b := &cliBox{t: t, dir: dir, instance: instance, project: "shop"}
	b.cli = buildTiffin(t, dir, "", "")
	good := buildTiffin(t, dir, "linux", "0.0.1-ssh")
	next := buildTiffin(t, dir, "linux", "0.0.2-ssh")
	b.env = append(os.Environ(), "TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"), "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	host := user + "@" + ip
	dry := b.ok("up", "--provider", "ssh", "--name", "dev-ssh", "--host", host, "--identity", key, "--data-disk", dev, "--dry-run")
	if dry["dryRun"] != true {
		t.Fatalf("dry run: %v", dry)
	}
	up := b.ok("up", "--provider", "ssh", "--name", "dev-ssh", "--host", host, "--identity", key, "--data-disk", dev, "--binary", good)
	domain := strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
	if up["url"] != "https://dashboard."+domain || up["ubuntu"] != release || !strings.HasPrefix(fmt.Sprint(up["login"]), "https://dashboard."+domain+"/login#") {
		t.Fatalf("up: %v", up)
	}
	t.Logf("up: %v (%vs)", up["url"], up["seconds"])
	phase("up", p)

	// ---- the server: data disk, hardening ----
	p = time.Now()
	checks := map[string]string{
		`findmnt -no FSTYPE,SOURCE /var/lib/tiffin | tr -s " "`:                                    "xfs " + dev,
		`sudo blkid -s LABEL -o value ` + dev:                                                      "tiffin-data",
		`sudo sshd -T | grep -E '^passwordauthentication '`:                                        "passwordauthentication no",
		`sudo sshd -T | grep -E '^permitrootlogin ' | sed 's/without-password/prohibit-password/'`: "permitrootlogin prohibit-password", // OpenSSH 10 (26.04) prints the new name
		`grep -c 'Unattended-Upgrade "1"' /etc/apt/apt.conf.d/20auto-upgrades`:                     "1",
		`systemctl is-enabled unattended-upgrades`:                                                 "enabled",
		`grep -c HC_Volume /etc/fstab || true`:                                                     "0",
		`swapon --show=NAME --noheadings`:                                                          "/swapfile",
		`test -f /etc/systemd/journald.conf.d/50-tiffin.conf && echo yes`:                          "yes",
		`sudo nft list table inet tiffin_guard >/dev/null && echo yes`:                             "yes",
		`systemctl is-active crowdsec crowdsec-firewall-bouncer | sort -u`:                         "active",
		`timedatectl show -p NTP --value`:                                                          "yes",
	}
	for script, want := range checks {
		if got := mustShell(script); !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want %q", script, got, want)
		}
	}
	// The address this computer reaches the server from is never banned.
	// The Mac reaches the vzNAT network from its gateway address (x.y.z.1).
	owner := ip[:strings.LastIndex(ip, ".")] + ".1"
	al := mustShell(`sudo cscli allowlists inspect tiffin-owner -o raw`)
	t.Logf("owner allowlist (this computer is %s):\n%s", owner, al)
	if !strings.Contains(al, strings.TrimSpace(owner)) {
		t.Errorf("owner allowlist lacks this computer: %s", al)
	}
	st := b.ok("status")
	names := map[string]bool{}
	for _, c := range st["checks"].([]any) {
		m := c.(map[string]any)
		names[fmt.Sprint(m["name"])] = true
		if m["name"] == "server" || m["name"] == "ssh protection" || m["name"] == "firewall" {
			t.Logf("check %-15s ok=%v  %s", m["name"], m["ok"], m["detail"])
			if m["ok"] != true {
				t.Errorf("check %s not ok: %v", m["name"], m["detail"])
			}
		}
	}
	if !names["server"] || !names["ssh protection"] {
		t.Errorf("status lacks the server checks: %v", names)
	}
	if st["ok"] != true {
		t.Logf("status not ok: %v", st)
	}
	phase("hardening", p)

	// ---- deploy a starter: Postgres + an app, over HTTPS on sslip.io ----
	p = time.Now()
	var tl struct {
		Templates []struct {
			ID, App  string
			Fragment struct {
				Apps     map[string]any `json:"apps"`
				Services map[string]any `json:"services"`
			}
		}
	}
	_, out := b.run("templates", "list")
	if err := json.Unmarshal([]byte(out), &tl); err != nil {
		t.Fatalf("templates: %v %s", err, out)
	}
	var app string
	for _, tp := range tl.Templates {
		if tp.ID != "hono-postgres" {
			continue
		}
		raw, _ := json.Marshal(map[string]any{"project": "shop", "apps": tp.Fragment.Apps, "services": tp.Fragment.Services})
		b.apply("shop", string(raw))
		b.waitReady("service/postgres")
		d := b.ok("deploys", "template", "shop", tp.App, "--template", tp.ID)
		live := waitDeploy(t, b, "shop", tp.App, fmt.Sprint(d["id"]), 10*time.Minute)
		app = fmt.Sprint(live["url"])
	}
	if !strings.Contains(app, domain) {
		t.Fatalf("the app's URL %q is not under %s", app, domain)
	}
	c := sslipClient(t, filepath.Join(dir, "config", "boxes", "dev-ssh", "ca.crt"), ip)
	waitGet(t, c, app, 2*time.Minute)
	phase("deploy", p)

	// ---- reboot: the data disk, services and app come back ----
	p = time.Now()
	// Reboot from inside, as on a server. Lima's vz backend may power the VM
	// off instead of rebooting; then start it again. Lima regenerates the SSH
	// host keys on every start (cloud-init), which a real server never does,
	// so only in that case the test forgets the old key.
	_, _ = shell("sudo systemctl reboot")
	time.Sleep(15 * time.Second)
	if st := lima(time.Minute, "list", instance, "--format", "{{.Status}}"); st != "Running" {
		t.Logf("lima stopped the VM on reboot (%s); starting it", st)
		lima(15*time.Minute, "start", instance, "--tty=false", "--timeout", "14m")
		kh := filepath.Join(dir, "config", "boxes", "dev-ssh", "known_hosts")
		_ = os.Remove(kh)
	}
	for i := 0; i < 60; i++ {
		if _, err := shell("true"); err == nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if ip2 := mustShell(`ip -4 -o addr show lima0 | awk '{print $4}' | cut -d/ -f1`); ip2 != ip {
		t.Fatalf("the server's address changed across the reboot (%s → %s)", ip, ip2)
	}
	waitHealthy(t, b, 3*time.Minute)
	if code, out := b.run("projects", "get", "shop"); code != 0 {
		t.Fatalf("project lost across reboot: %s", out)
	}
	waitGet(t, c, app, 3*time.Minute)
	phase("reboot", p)

	// ---- update: tiffin up --name finds the server again ----
	p = time.Now()
	b.ok("up", "--name", "dev-ssh", "--binary", next)
	if h := b.ok("health"); h["version"] != "0.0.2-ssh" {
		t.Fatalf("after update: %v", h)
	}
	phase("update", p)

	// ---- down: Tiffin stops, the server and its data stay ----
	p = time.Now()
	if code, out := b.run("down", "--confirm", "nope"); code == 0 {
		t.Fatalf("down of an unknown box must fail: %s", out)
	}
	if code, out := b.run("down"); code != 4 {
		t.Fatalf("down without --confirm must exit 4: %d %s", code, out)
	}
	b.ok("down", "--confirm", "dev-ssh")
	if s, _ := shell(`systemctl is-active tiffin`); s == "active" {
		t.Fatal("tiffin still runs after down")
	}
	if got := mustShell(`sudo test -f /var/lib/tiffin/platform/owner-token && echo kept`); got != "kept" {
		t.Fatal("down must keep the data on a server Tiffin did not create")
	}
	phase("down", p)
	t.Logf("TOTAL ssh provider on Ubuntu %s: %s", release, time.Since(start).Round(time.Second))
}

// sslipClient trusts the box's CA and dials the server's IP for any name.
func sslipClient(t *testing.T, caFile, ip string) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			return d.DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
	}}
}

func waitGet(t *testing.T, c *http.Client, u string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var last string
	for time.Now().Before(deadline) {
		res, err := c.Get(u)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return
			}
			last = res.Status
		} else {
			last = err.Error()
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("GET %s: %s", u, last)
}

func waitHealthy(t *testing.T, b *cliBox, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if code, out := b.run("health"); code == 0 && strings.Contains(out, `"ok"`) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("box not healthy in time")
}
