package cli

import (
	"context"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/provider/hetzner"
	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/btahir/tiffin/internal/provider/remote"
)

func TestSSLIP(t *testing.T) {
	if d := sslipDomain("203.0.113.5"); d != "203-0-113-5.sslip.io" {
		t.Fatal(d)
	}
	if d := sslipDomain("2001:db8:1::1"); d != "2001-db8-1--1.sslip.io" {
		t.Fatal(d)
	}
	for host, want := range map[string]string{
		"dashboard.203-0-113-5.sslip.io": "203.0.113.5",
		"shop.2001-db8-1--1.sslip.io":    "2001:db8:1::1",
		"203-0-113-5.sslip.io":           "203.0.113.5",
	} {
		a, ok := sslipAddr(host)
		if !ok || a.String() != want {
			t.Errorf("%s: %v %v", host, a, ok)
		}
	}
	for _, host := range []string{"dashboard.tiffin.localhost", "example.com", "x.not-an-ip.sslip.io"} {
		if _, ok := sslipAddr(host); ok {
			t.Errorf("%s is not an sslip address", host)
		}
	}
}

func TestUpHetznerDryRun(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	env := newEnv(t)
	env["TIFFIN_CONFIG_DIR"] = filepath.Join(t.TempDir(), "cfg")
	env["HCLOUD_ENDPOINT"] = f.URL
	env["HCLOUD_TOKEN"] = hetznertest.Token
	old := publicIPLookup
	publicIPLookup = func(context.Context) []netip.Addr { return []netip.Addr{netip.MustParseAddr("198.51.100.7")} }
	defer func() { publicIPLookup = old }()

	code, out, errOut := run(t, env, "up", "--provider", "hetzner", "--name", "shop", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: %d %s %s", code, out, errOut)
	}
	var res struct {
		DryRun bool          `json:"dryRun"`
		Plan   *hetzner.Plan `json:"plan"`
	}
	if err := json.Unmarshal(out, &res); err != nil || !res.DryRun || res.Plan == nil {
		t.Fatalf("%v %s", err, out)
	}
	if res.Plan.MonthlyNet != 7.39 || res.Plan.ServerType != "cax11" || res.Plan.Arch != "arm64" || !strings.Contains(strings.Join(res.Plan.SSHFrom, ","), "198.51.100.7") {
		t.Fatalf("plan %+v", res.Plan)
	}
	if len(f.Mutations) != 0 {
		t.Fatalf("dry run changed %v", f.Mutations)
	}

	if code, out, _ = run(t, env, "up", "--provider", "hetzner", "--name", "shop", "--ssh-from", "any", "--dry-run"); code != 0 || !strings.Contains(string(out), `"anywhere"`) {
		t.Fatalf("--ssh-from any: %d %s", code, out)
	}
	if code, out, _ = run(t, env, "up", "--provider", "hetzner", "--ssh-from", "any,198.51.100.7", "--dry-run"); code != ExitInvalid {
		t.Fatalf("any with addresses: %d %s", code, out)
	}

	// Mistakes are caught before anything is created.
	code, out, _ = run(t, env, "up", "--provider", "hetzner", "--location", "ash", "--dry-run")
	if code != ExitError || !strings.Contains(string(out), "cannot be ordered in ash") {
		t.Fatalf("cax11 in ash: %d %s", code, out)
	}
	code, out, _ = run(t, env, "up", "--provider", "hetzner", "--reboot-window", "4am", "--dry-run")
	if code != ExitInvalid || !strings.Contains(string(out), "reboot window") {
		t.Fatalf("bad window: %d %s", code, out)
	}
	delete(env, "HCLOUD_TOKEN")
	if code, out, _ = run(t, env, "up", "--provider", "hetzner", "--dry-run"); code != ExitInvalid || !strings.Contains(string(out), "HCLOUD_TOKEN") {
		t.Fatalf("no token: %d %s", code, out)
	}
}

func TestDownHetznerShowsThenDeletes(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	dir := t.TempDir()
	hp, err := hetzner.New(hetzner.Config{Token: hetznertest.Token, Endpoint: f.URL, Name: "shop", SSHFrom: []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")},
		KeyPath: filepath.Join(dir, "k"), KnownHosts: filepath.Join(dir, "kh")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hp.Ensure(ctx, func(string) {}); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t)
	env["TIFFIN_CONFIG_DIR"] = filepath.Join(t.TempDir(), "cfg")
	env["HCLOUD_ENDPOINT"] = f.URL
	env["HCLOUD_TOKEN"] = hetznertest.Token

	// Nothing labelled for this name: nothing to delete.
	mustRun(t, env, ExitOK, "down", "--provider", "hetzner", "--confirm", "nope")
	if code, out, _ := run(t, env, "down", "--provider", "hetzner", "--confirm", "shop", "--token-file", "/nonexistent"); code != ExitInvalid {
		t.Fatalf("bad token file: %d %s", code, out)
	}

	// The box as up left it on this computer; down without --confirm previews.
	a := &app{io: IO{Env: func(k string) string { return env[k] }}}
	if err := a.saveBoxes(&boxesFile{Current: "shop", Boxes: map[string]*boxConfig{"shop": {Provider: "hetzner", Server: &serverBox{}}}}); err != nil {
		t.Fatal(err)
	}
	pv := decode(t, mustRun(t, env, ExitConfirm, "down"))
	if len(pv["delete"].([]any)) != 3 || len(pv["keep"].([]any)) != 1 || !strings.Contains(string(mustJSON(pv["keep"])), "2.29 EUR a month") {
		t.Fatalf("preview: %v", pv)
	}
	if s, v, fw, k := f.Count(); s != 1 || v != 1 || fw != 1 || k != 1 {
		t.Fatalf("nothing may be deleted yet: %d %d %d %d", s, v, fw, k)
	}

	out := mustRun(t, env, ExitOK, "down", "--confirm", "shop")
	res := decode(t, out)
	if res["destroyed"] != "shop" || !strings.Contains(string(out), "with your data") {
		t.Fatalf("down: %s", out)
	}
	if s, v, fw, k := f.Count(); s != 0 || v != 1 || fw != 0 || k != 0 {
		t.Fatalf("after down: %d %d %d %d", s, v, fw, k)
	}
	mustRun(t, env, ExitOK, "down", "--provider", "hetzner", "--confirm", "shop", "--delete-data")
	if s, v, fw, k := f.Count(); s+v+fw+k != 0 {
		t.Fatal("everything must be gone")
	}
}

func TestUpHetznerAdoptDryRun(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	f.AddHandmadeServer("shiptiffin-server")
	env := newEnv(t)
	env["TIFFIN_CONFIG_DIR"] = filepath.Join(t.TempDir(), "cfg")
	env["HCLOUD_ENDPOINT"] = f.URL
	env["HCLOUD_TOKEN"] = hetznertest.Token
	args := []string{"up", "--provider", "hetzner", "--adopt", "shiptiffin-server", "--ssh-from", "198.51.100.7", "--dry-run"}
	if code, out, _ := run(t, env, args...); code != ExitInvalid || !strings.Contains(string(out), "--ssh-key") {
		t.Fatalf("adopt without a key: %d %s", code, out)
	}
	key := filepath.Join(t.TempDir(), "id")
	pub, err := remote.GenerateKey(key, "me")
	if err != nil {
		t.Fatal(err)
	}
	f.AddKey("mine", pub)
	env["HCLOUD_SSH_KEY"] = key
	out := mustRun(t, env, ExitOK, args...)
	var res struct {
		Adopt *hetzner.AdoptPlan `json:"adopt"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.Adopt == nil || res.Adopt.Server != "shiptiffin-server" || len(res.Adopt.Changes) < 5 {
		t.Fatalf("%v %s", err, out)
	}
	if len(f.Mutations) != 0 {
		t.Fatalf("a dry run changed %v", f.Mutations)
	}
}

func mustRun(t *testing.T, env map[string]string, want int, args ...string) []byte {
	t.Helper()
	code, out, errOut := run(t, env, args...)
	if code != want {
		t.Fatalf("tiffin %v: exit %d, want %d\n%s%s", args, code, want, out, errOut)
	}
	return out
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
