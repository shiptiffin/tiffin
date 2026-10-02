//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestNaming(t *testing.T) {
	n := newName()
	ts, ok := createdAt(n)
	if !ok || time.Since(ts) > time.Minute || time.Since(ts) < -time.Minute {
		t.Errorf("createdAt(%q) = %v, %v", n, ts, ok)
	}
	if _, ok := createdAt("other-vm"); ok {
		t.Error("createdAt accepted a foreign name")
	}
	d := newDiskName()
	if len(d) != 7 || !isE2EDisk(d) {
		t.Errorf("disk name %q must be 7 chars and recognised by isE2EDisk (XFS label limit)", d)
	}
	for _, bad := range []string{"data", "e2e", "e2ezzzz", "xyz1234"} {
		if isE2EDisk(bad) {
			t.Errorf("isE2EDisk(%q) = true", bad)
		}
	}
}
