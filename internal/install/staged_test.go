package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A new build writes its unit files for the same box: the options come
// back from the installed tiffin.service.
func TestOptionsFromUnit(t *testing.T) {
	for _, o := range []Options{
		{Domain: "tiffin.localhost", HTTPSPort: 8443, HTTPPort: 8080},
		{Domain: "tiffin.localhost", HTTPSPort: 8443, HTTPPort: 8080, PublicPort: 18443},
		{Domain: "shop.example.com", HTTPSPort: 443, HTTPPort: 80, PublicIP: "203.0.113.7", PublicIPv6: "2001:db8::7"},
	} {
		got, err := OptionsFromUnit(Unit(o))
		if err != nil {
			t.Fatal(err)
		}
		if got != o {
			t.Errorf("options:\n got %+v\nwant %+v", got, o)
		}
		if Unit(got) != Unit(o) {
			t.Error("the unit changed")
		}
	}
	if _, err := OptionsFromUnit("[Service]\nExecStart=/usr/local/bin/tiffin serve\n"); err == nil {
		t.Fatal("a unit without the box's options")
	}
}

func TestWriteUnits(t *testing.T) {
	dir := t.TempDir()
	units, backup := filepath.Join(dir, "units"), filepath.Join(dir, "backup")
	_ = os.MkdirAll(units, 0o755)
	o := Options{Domain: "shop.example.com", HTTPSPort: 443, HTTPPort: 80, PublicIP: "203.0.113.7"}
	old := strings.Replace(Unit(o), "LimitNOFILE=1048576", "LimitNOFILE=4096", 1) // as an older build wrote it
	if err := os.WriteFile(filepath.Join(units, "tiffin.service"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	sysctl := func(_ context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if err := writeUnits(context.Background(), sysctl, units, backup); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"tiffin.service": Unit(o), "tiffin-edge.service": EdgeUnit(), "tiffin-edge.socket": EdgeSocketUnit(o)} {
		if b, _ := os.ReadFile(filepath.Join(units, name)); string(b) != want {
			t.Errorf("%s:\n%s", name, b)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(backup, "tiffin.service")); string(b) != old {
		t.Error("the previous unit is not in the backup")
	}
	if got := strings.Join(calls, "|"); got != "daemon-reload|enable tiffin.service tiffin-edge.service tiffin-edge.socket" {
		t.Errorf("systemctl: %s", got)
	}
}

// An update that changes the edge restarts it once the new build is
// healthy; when the edge does not come back, the update rolls back and
// the edge restarts again, on the previous build.
func TestUpdateRestartsTheEdge(t *testing.T) {
	u, _, dir := setup(t)
	ctx := context.Background()
	if err := u.Update(ctx, build(t, dir, "v1", "build one")); err != nil {
		t.Fatal(err)
	}
	var edge []string
	u.RestartEdge = func(context.Context) error {
		edge = append(edge, current(t, u))
		if current(t, u) == "edge breaks" {
			return errors.New("the edge did not keep running")
		}
		return nil
	}
	if err := u.Update(ctx, build(t, dir, "v2", "build two")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(edge, ",") != "build two" {
		t.Fatalf("edge restarts: %v", edge)
	}
	edge = nil
	err := u.Update(ctx, build(t, dir, "v3", "edge breaks"))
	if !errors.Is(err, ErrRolledBack) || !strings.Contains(err.Error(), "edge") {
		t.Fatalf("want a rollback naming the edge, got %v", err)
	}
	if got := current(t, u); got != "build two" {
		t.Fatalf("after rollback current = %q", got)
	}
	if strings.Join(edge, ",") != "edge breaks,build two" {
		t.Fatalf("edge restarts: %v", edge)
	}
	// A build that is unhealthy never gets the edge.
	edge = nil
	if err := u.Update(ctx, build(t, dir, "bad", "BROKEN")); !errors.Is(err, ErrRolledBack) {
		t.Fatal(err)
	}
	if len(edge) != 0 {
		t.Fatalf("the edge restarted for an unhealthy build: %v", edge)
	}
}

func TestWriteResult(t *testing.T) {
	dir := t.TempDir()
	if err := WriteResult(dir, StagedResult{ID: "upd_1", Status: "rolled-back", Error: "x"}); err != nil {
		t.Fatal(err)
	}
	var r StagedResult
	raw, _ := os.ReadFile(filepath.Join(dir, "upd_1.result.json"))
	if json.Unmarshal(raw, &r) != nil || r.Status != "rolled-back" {
		t.Fatalf("%s", raw)
	}
}
