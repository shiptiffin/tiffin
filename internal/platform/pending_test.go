package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlan(t *testing.T, root string, plan PendingImport) {
	t.Helper()
	b, _ := json.Marshal(plan)
	if err := os.MkdirAll(filepath.Dir(PendingImportPath(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PendingImportPath(root), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestApplyPendingImport(t *testing.T) {
	root := t.TempDir()
	var units []string
	defer func(orig func(string, string) error) { runUnit = orig }(runUnit)
	runUnit = func(verb, unit string) error { units = append(units, verb+" "+unit); return nil }
	home := filepath.Join(root, "platform")
	pend := filepath.Join(root, "portable", "pending")
	put(t, filepath.Join(home, "state.db"), "old state")
	put(t, filepath.Join(home, "state.db-wal"), "old wal")
	put(t, filepath.Join(pend, "state.db"), "new state")
	put(t, filepath.Join(root, "analytics", "analytics.db"), "old analytics")
	put(t, filepath.Join(root, "analytics", "dbip.mmdb"), "geo of this box")
	put(t, filepath.Join(pend, "files", "analytics", "analytics.db"), "new analytics")
	put(t, filepath.Join(pend, "files", "email", "messages", "m1"), "new mail") // no email dir here yet

	if ran, err := ApplyPendingImport(root, func(s string) { t.Log(s) }); ran || err != nil {
		t.Fatalf("no plan: ran=%v err=%v", ran, err)
	}
	writePlan(t, root, PendingImport{Import: "im_1", Aside: filepath.Join(root, "portable", "pre-import-im_1"),
		StopUnits: []string{"tiffin-storage.service"}, StartUnits: []string{"tiffin-storage.service"},
		Swaps: []PendingSwap{
			{From: filepath.Join(pend, "state.db"), To: filepath.Join(home, "state.db")},
			{To: filepath.Join(home, "state.db-wal")},
			{To: filepath.Join(home, "state.db-shm")}, // absent: nothing to do
			{From: filepath.Join(pend, "files", "analytics"), To: filepath.Join(root, "analytics"), Keep: []string{"*.mmdb"}},
			{From: filepath.Join(pend, "files", "email"), To: filepath.Join(root, "email")},
		}})
	ran, err := ApplyPendingImport(root, func(s string) { t.Log(s) })
	if !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if read(t, filepath.Join(home, "state.db")) != "new state" {
		t.Fatal("state not swapped")
	}
	if _, err := os.Stat(filepath.Join(home, "state.db-wal")); err == nil {
		t.Fatal("the old WAL must move aside with the old database")
	}
	if read(t, filepath.Join(root, "analytics", "analytics.db")) != "new analytics" || read(t, filepath.Join(root, "analytics", "dbip.mmdb")) != "geo of this box" {
		t.Fatal("analytics swap with keep")
	}
	if read(t, filepath.Join(root, "email", "messages", "m1")) != "new mail" {
		t.Fatal("new directory")
	}
	if read(t, filepath.Join(root, "portable", "pre-import-im_1", "0-state.db")) != "old state" {
		t.Fatal("the old state must be kept aside")
	}
	if strings.Join(units, ",") != "stop tiffin-storage.service,start tiffin-storage.service" {
		t.Fatalf("units: %v", units)
	}
	var res PendingResult
	_ = json.Unmarshal([]byte(read(t, PendingResultPath(root))), &res)
	if !res.OK || res.Import != "im_1" || res.Swapped != 5 {
		t.Fatalf("result: %+v", res)
	}
	// It never runs twice.
	if ran, _ := ApplyPendingImport(root, func(s string) { t.Log(s) }); ran {
		t.Fatal("plan ran twice")
	}
}

func TestApplyPendingImportRollsBack(t *testing.T) {
	root := t.TempDir()
	defer func(orig func(string, string) error) { runUnit = orig }(runUnit)
	runUnit = func(string, string) error { return nil }
	home := filepath.Join(root, "platform")
	pend := filepath.Join(root, "portable", "pending")
	put(t, filepath.Join(home, "state.db"), "old state")
	put(t, filepath.Join(home, "secrets.key"), "old key")
	put(t, filepath.Join(pend, "state.db"), "new state")
	put(t, filepath.Join(root, "analytics", "dbip.mmdb"), "geo")
	put(t, filepath.Join(pend, "files", "analytics", "analytics.db"), "new")
	writePlan(t, root, PendingImport{Import: "im_2", Aside: filepath.Join(root, "portable", "pre-import-im_2"),
		Swaps: []PendingSwap{
			{From: filepath.Join(pend, "state.db"), To: filepath.Join(home, "state.db")},
			{From: filepath.Join(pend, "files", "analytics"), To: filepath.Join(root, "analytics"), Keep: []string{"*.mmdb"}},
			{From: filepath.Join(pend, "secrets.key"), To: filepath.Join(home, "secrets.key")}, // staged key missing: fails
		}})
	ran, err := ApplyPendingImport(root, func(s string) { t.Log(s) })
	if !ran || err == nil {
		t.Fatalf("want a failure: ran=%v err=%v", ran, err)
	}
	if read(t, filepath.Join(home, "state.db")) != "old state" || read(t, filepath.Join(home, "secrets.key")) != "old key" {
		t.Fatal("the old state must be back")
	}
	if read(t, filepath.Join(root, "analytics", "dbip.mmdb")) != "geo" {
		t.Fatal("kept files must be back in place")
	}
	if _, err := os.Stat(filepath.Join(root, "analytics", "analytics.db")); err == nil {
		t.Fatal("the staged analytics must not stay in place")
	}
	var res PendingResult
	_ = json.Unmarshal([]byte(read(t, PendingResultPath(root))), &res)
	if res.OK || !strings.Contains(res.Error, "secrets.key") {
		t.Fatalf("result: %+v", res)
	}
}
