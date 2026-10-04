//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/provider/hetzner"
)

// TestHetzner creates a real CAX11 box on Hetzner Cloud and destroys it.
// It costs a few cents and is skipped unless HCLOUD_TOKEN is set (a read &
// write token of a project you are happy to use for tests). HCLOUD_SSH_KEY
// (your own key) and HCLOUD_LOCATION are honoured; TIFFIN_E2E_IMAGE picks
// ubuntu-26.04 instead of the default.
//
//	tiffin up --provider hetzner (dry run, then for real) → HTTPS on
//	dashboard.<ip>.sslip.io → deploy a starter (Postgres + app) → reboot
//	the server → update → tiffin down --delete-data → nothing labelled
//	tiffin-box=<name> is left in the project.
func TestHetzner(t *testing.T) {
	token := os.Getenv("HCLOUD_TOKEN")
	if token == "" {
		t.Skip("HCLOUD_TOKEN not set; skipping the real Hetzner test")
	}
	start := time.Now()
	phase := phaseLogger(t)
	dir := t.TempDir()
	var rb [3]byte
	_, _ = rand.Read(rb[:])
	name := "tiffin-e2e-" + hex.EncodeToString(rb[:])
	ctx := context.Background()

	// The test's own view of the project, for the reboot and the final check.
	hp, err := hetzner.New(hetzner.Config{Token: token, Endpoint: os.Getenv("HCLOUD_ENDPOINT"), Name: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") == "1" {
			t.Logf("TIFFIN_E2E_KEEP=1: leaving Hetzner box %s (it costs money: tiffin down --provider hetzner --confirm %s --delete-data)", name, name)
			return
		}
		if _, err := hp.DestroyAll(ctx, true, func(s string) { t.Log(s) }); err != nil {
			t.Errorf("cleanup: %v (delete resources labelled tiffin-box=%s by hand)", err, name)
		}
	})

	b := &cliBox{t: t, dir: dir, project: "shop"}
	b.cli = buildTiffin(t, dir, "", "")
	good := buildArch(t, dir, "arm64", "0.0.1-hz")
	next := buildArch(t, dir, "arm64", "0.0.2-hz")
	b.env = append(os.Environ(), "TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"), "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	args := []string{"up", "--provider", "hetzner", "--name", name, "--type", "cax11"}
	if img := os.Getenv("TIFFIN_E2E_IMAGE"); img != "" {
		args = append(args, "--image", img)
	}

	// ---- dry run: prices, nothing created ----
	p := time.Now()
	dry := b.ok(append(args, "--dry-run")...)
	plan, _ := dry["plan"].(map[string]any)
	if dry["dryRun"] != true || plan == nil || plan["monthlyNet"].(float64) <= 0 {
		t.Fatalf("dry run: %v", dry)
	}
	t.Logf("dry run: %.2f %s/month net (%v)", plan["monthlyNet"], plan["currency"], plan["costs"])
	if in, err := hp.Inventory(ctx); err != nil || !in.Empty() {
		t.Fatalf("a dry run created something: %+v %v", in, err)
	}
	phase("dry-run", p)

	// ---- up ----
	p = time.Now()
	up := b.ok(append(args, "--binary", good)...)
	ip := fmt.Sprint(up["ip"])
	domain := strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
	if up["url"] != "https://dashboard."+domain {
		t.Fatalf("up: %v", up)
	}
	t.Logf("up: %v in %vs", up["url"], up["seconds"])
	c := sslipClient(t, filepath.Join(dir, "config", "boxes", name, "ca.crt"), ip)
	waitGet(t, c, "https://dashboard."+domain+"/v1/health", time.Minute)
	for _, ck := range b.ok("status")["checks"].([]any) {
		m := ck.(map[string]any)
		if m["name"] == "server" || m["name"] == "ssh protection" || m["name"] == "firewall" {
			t.Logf("check %-15s ok=%v  %s", m["name"], m["ok"], m["detail"])
			if m["ok"] != true {
				t.Errorf("check %s: %v", m["name"], m["detail"])
			}
		}
	}
	phase("up", p)

	// ---- a starter: Postgres + app ----
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
	_ = json.Unmarshal([]byte(out), &tl)
	var app string
	for _, tp := range tl.Templates {
		if tp.ID == "hono-postgres" {
			raw, _ := json.Marshal(map[string]any{"project": "shop", "apps": tp.Fragment.Apps, "services": tp.Fragment.Services})
			b.apply("shop", string(raw))
			b.waitReady("service/postgres")
			d := b.ok("deploys", "template", "shop", tp.App, "--template", tp.ID)
			app = fmt.Sprint(waitDeploy(t, b, "shop", tp.App, fmt.Sprint(d["id"]), 15*time.Minute)["url"])
		}
	}
	waitGet(t, c, app, 2*time.Minute)
	phase("deploy", p)

	// ---- reboot ----
	p = time.Now()
	if err := hp.Reboot(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Second)
	waitHealthy(t, b, 5*time.Minute)
	waitGet(t, c, app, 3*time.Minute)
	phase("reboot", p)

	// ---- update ----
	p = time.Now()
	b.ok("up", "--name", name, "--binary", next)
	if h := b.ok("health"); h["version"] != "0.0.2-hz" {
		t.Fatalf("after update: %v", h)
	}
	phase("update", p)

	// ---- down ----
	p = time.Now()
	if code, out := b.run("down"); code != 4 || !strings.Contains(out, "server "+name) {
		t.Fatalf("down preview: %d %s", code, out)
	}
	b.ok("down", "--confirm", name, "--delete-data")
	in, err := hp.Inventory(ctx)
	if err != nil || !in.Empty() {
		t.Fatalf("labelled resources left after down: %+v %v", in, err)
	}
	phase("down", p)
	t.Logf("TOTAL hetzner: %s", time.Since(start).Round(time.Second))
}

// buildArch builds the linux tiffin for a given architecture.
func buildArch(t *testing.T, dir, arch, version string) string {
	t.Helper()
	out := filepath.Join(dir, "tiffin-linux-"+arch+"-"+version)
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, "-ldflags", "-X github.com/btahir/tiffin/internal/version.Version="+version, "./cmd/tiffin")
	cmd.Dir, cmd.Env = RepoRoot(), append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, o)
	}
	return out
}
