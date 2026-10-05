package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/provider"
	"github.com/btahir/tiffin/internal/provider/hetzner"
	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
)

// resizeEnv is a Hetzner box "shop" (cax11, 40 GB volume) this computer knows.
func resizeEnv(t *testing.T) (*hetznertest.Fake, map[string]string, *hetzner.Provider) {
	t.Helper()
	f := hetznertest.New()
	t.Cleanup(f.Close)
	dir := t.TempDir()
	hp, err := hetzner.New(hetzner.Config{Token: hetznertest.Token, Endpoint: f.URL, Name: "shop", SSHFrom: []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")},
		KeyPath: filepath.Join(dir, "k"), KnownHosts: filepath.Join(dir, "kh"), PollInterval: 10e6})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hp.Ensure(context.Background(), func(string) {}); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t)
	env["TIFFIN_CONFIG_DIR"] = filepath.Join(t.TempDir(), "cfg")
	env["HCLOUD_ENDPOINT"] = f.URL
	env["HCLOUD_TOKEN"] = hetznertest.Token
	a := &app{io: IO{Env: func(k string) string { return env[k] }}}
	if err := a.saveBoxes(&boxesFile{Current: "shop", Boxes: map[string]*boxConfig{"shop": {Provider: "hetzner", URL: "https://dashboard.203-0-113-1.sslip.io",
		Server: &serverBox{Location: "fsn1", ServerType: "cax11", VolumeGB: 40, Identity: filepath.Join(dir, "k")}}}}); err != nil {
		t.Fatal(err)
	}
	return f, env, hp
}

func TestUpResizeAsksFirst(t *testing.T) {
	f, env, _ := resizeEnv(t)
	made := len(f.Mutations)
	up := []string{"up", "--name", "shop", "--ssh-from", "198.51.100.7"}

	// An agent gets the plan and exit 4; nothing changes.
	pv := decode(t, mustRun(t, env, ExitConfirm, append(up, "--type", "cax21", "--volume-size", "80")...))
	plan, _ := pv["plan"].(map[string]any)
	if pv["code"] != "confirm_required" || plan == nil || plan["to"].(map[string]any)["name"] != "cax21" || plan["volumeToGB"] != 80.0 ||
		!strings.Contains(pv["hint"].(string), "tiffin up --name shop --type cax21 --volume-size 80 --yes") || !strings.Contains(pv["detail"].(string), "about 2 minutes") {
		t.Fatalf("confirm: %v", pv)
	}

	// --dry-run shows the same plan.
	var dry struct {
		DryRun bool            `json:"dryRun"`
		Resize *hetzner.Resize `json:"resize"`
	}
	if err := json.Unmarshal(mustRun(t, env, ExitOK, append(up, "--volume-size", "100", "--dry-run")...), &dry); err != nil || !dry.DryRun || dry.Resize.Downtime || dry.Resize.VolumeToGB != 100 {
		t.Fatalf("dry run: %+v %v", dry.Resize, err)
	}

	// Refusals are validation errors, said plainly.
	for args, want := range map[string]string{
		"--type cx33":       "Hetzner cannot move a server between ARM and x86",
		"--volume-size 20":  "volumes cannot shrink",
		"--type nonesuch-9": "unknown Hetzner server type",
	} {
		code, out, _ := run(t, env, append(up, strings.Fields(args)...)...)
		if code != ExitInvalid || !strings.Contains(string(out), want) {
			t.Errorf("%s: want exit 3 with %q, got %d %s", args, want, code, out)
		}
	}

	// A person at a terminal is asked; anything but yes changes nothing.
	var out bytes.Buffer
	yes := true
	env["NO_COLOR"] = "1"
	code := Execute(context.Background(), append(up, "--type", "cax21"), IO{Out: &out, Err: &out, TTY: &yes, In: strings.NewReader("no\n"), Env: func(k string) string { return env[k] }})
	if code != ExitConfirm || !strings.Contains(out.String(), "cax11 (2 vCPU ARM, 4 GB RAM, 40 GB disk) → cax21 (4 vCPU ARM, 8 GB RAM, 80 GB disk)") ||
		!strings.Contains(out.String(), "7.39 → 10.89") || !strings.Contains(out.String(), "Nothing was changed.") {
		t.Fatalf("asked: %d\n%s", code, out.String())
	}
	if got := f.Mutations[made:]; len(got) != 0 {
		t.Fatalf("nothing may change before yes: %v", got)
	}
}

// fakeMachine records the scripts run on it.
type fakeMachine struct{ scripts *[]string }

func (m fakeMachine) Exec(_ context.Context, s string) (string, string, error) {
	*m.scripts = append(*m.scripts, s)
	return "", "", nil
}
func (fakeMachine) Copy(context.Context, string, string) error { return nil }
func (fakeMachine) Arch() string                               { return "arm64" }

func TestResizeBoxStopsTiffinThenChangesTheType(t *testing.T) {
	f, env, hp := resizeEnv(t)
	ctx := context.Background()
	r, err := hp.PlanResize(ctx, "cax21", 80)
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	a := &app{io: IO{Env: func(k string) string { return env[k] }}}
	reach := func() (provider.Machine, error) {
		log = append(log, "ssh")
		return fakeMachine{&log}, nil
	}
	before := len(f.Mutations)
	if err := a.resizeBox(ctx, hp, r, reach); err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, mu := range f.Mutations[before:] {
		calls = append(calls, mu[strings.LastIndex(mu, "/")+1:])
	}
	if !slices.Equal(calls, []string{"resize", "shutdown", "change_type", "poweron"}) || !slices.Equal(log, []string{"ssh", "sudo systemctl stop tiffin; sync"}) {
		t.Fatalf("hetzner %v, server %q", calls, log)
	}
	if f.Volume().Size != 80 || f.ServerByName("shop").ServerType.Name != "cax21" || !slices.Equal(f.ChangeTypes, []string{"cax21 upgrade_disk=false"}) {
		t.Fatalf("after: volume %d, type %s, %v", f.Volume().Size, f.ServerByName("shop").ServerType.Name, f.ChangeTypes)
	}

	// What the dashboard gets: the new size and the next types up.
	m := machineInfo(ctx, hp)
	if m == nil || m.ServerType.Name != "cax21" || m.VolumeGB != 80 || len(m.Upgrades) != 2 || m.Upgrades[0].Name != "cax31" || m.Upgrades[0].MemoryGB != 16 {
		t.Fatalf("machine %+v", m)
	}

	// Growing the volume alone never touches the server.
	r, _ = hp.PlanResize(ctx, "", 120)
	log, before = nil, len(f.Mutations)
	if err := a.resizeBox(ctx, hp, r, reach); err != nil || len(log) != 0 || len(f.Mutations[before:]) != 1 {
		t.Fatalf("volume only: %v %v %v", err, log, f.Mutations[before:])
	}
}
