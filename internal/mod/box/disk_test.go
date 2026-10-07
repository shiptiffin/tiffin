package box

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The disk report names each folder of the data disk with its size, and
// counts containerd as what the others leave of the used space.
func TestMeasureDisk(t *testing.T) {
	data := t.TempDir()
	write := func(rel string, n int) {
		p := filepath.Join(data, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("postgres/18/base/1", 3000)
	write("backups/sets/bk_1/x", 2000)
	write("mystery/x", 10)
	if err := os.MkdirAll(filepath.Join(data, "containerd"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep := measureDisk(context.Background(), newSampler("", data))
	got := map[string]DiskPart{}
	for _, p := range rep.Parts {
		got[p.Name] = p
	}
	if got["postgres"].Bytes != 3000 || got["postgres"].What != diskParts["postgres"] || got["backups"].Bytes != 2000 || got["mystery"].What != "Other" {
		t.Fatalf("parts: %+v", rep.Parts)
	}
	if _, ok := got["containerd"]; !ok || rep.Disk.TotalBytes == 0 {
		t.Fatalf("containerd part or disk missing: %+v", rep)
	}
	if rep.Parts[0].Bytes < rep.Parts[len(rep.Parts)-1].Bytes {
		t.Fatal("parts not biggest first")
	}
	if humanSize(2_500_000_000) != "2.5 GB" || humanSize(1500) != "1.5 kB" {
		t.Fatal(humanSize(2_500_000_000))
	}
}
