package change

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// WithTitle changes only the title in the project resource; a plan that
// changes nothing else says so, and the round trip through a manifest keeps it.
func TestWithTitle(t *testing.T) {
	keep := 5
	m := manifest.Normalize(&manifest.Manifest{Project: "website", SleepAfter: "7d", KeepVersions: &keep})
	res, err := Resources(m)
	if err != nil {
		t.Fatal(err)
	}
	cur := map[string]Resource{}
	for k, v := range res {
		cur[k] = v
	}
	titled, err := WithTitle(cur, "ShipTiffin website")
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectTitle(titled); got != "ShipTiffin website" {
		t.Fatalf("title %q", got)
	}
	ops := Diff(res, titled)
	if len(ops) != 1 || ops[0].Address != KindProject || ops[0].Action != Update {
		t.Fatalf("ops: %+v", ops)
	}
	if tier, why := Classify(ops[0]); tier != TierReversible || !strings.Contains(why, "title") {
		t.Fatalf("risk: %s %s", tier, why)
	}
	var ps ProjectSpec
	_ = json.Unmarshal(titled[KindProject].Spec, &ps)
	if ps.SleepAfter != "7d" || ps.KeepVersions == nil || *ps.KeepVersions != 5 {
		t.Fatalf("other settings lost: %+v", ps)
	}
	back, err := ManifestFromResources("website", titled)
	if err != nil || back.Title != "ShipTiffin website" || back.Project != "website" {
		t.Fatalf("manifest: %+v %v", back, err)
	}
	cleared, _ := WithTitle(titled, "")
	if ops := Diff(res, cleared); len(ops) != 0 {
		t.Fatalf("clearing left %d ops", len(ops))
	}
}
