package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A limited project's builds: the build cgroup holds them to its share while
// they run, static builds get --cpus, and nothing changes without a limit.
func TestBuildLimit(t *testing.T) {
	if (buildLimit{}).staticBuildArgs() != nil {
		t.Fatal("no limit, no flags")
	}
	if buildLimitFor("nobody") != (buildLimit{}) {
		t.Fatal("a project the budget does not know has no build limit")
	}
	l := buildLimit{pct: 25, cpus: 0.5, highMB: minBuildMB}
	if got := l.staticBuildArgs(); !slices.Equal(got, []string{"--cpus", "0.5"}) {
		t.Fatalf("static args: %v", got)
	}
	dir := t.TempDir()
	for _, f := range []string{"cpu.max", "memory.high"} {
		_ = os.WriteFile(filepath.Join(dir, f), []byte("max"), 0o644)
	}
	undo, err := l.apply(dir)
	if err != nil {
		t.Fatal(err)
	}
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(dir, f)); return string(b) }
	if read("cpu.max") != "50000 100000" || read("memory.high") != "1073741824" {
		t.Fatalf("during the build: cpu.max %q memory.high %q", read("cpu.max"), read("memory.high"))
	}
	undo()
	if read("cpu.max") != "max 100000" || read("memory.high") != "max" {
		t.Fatalf("after the build: cpu.max %q memory.high %q", read("cpu.max"), read("memory.high"))
	}
	if w := l.words("shop"); !strings.Contains(w, "shop is limited to 25% of the box") || !strings.Contains(w, "0.5 CPUs") {
		t.Fatal(w)
	}
	// No cgroup to write (not a box): the build goes on unlimited.
	if _, err := l.apply(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected an error without the cgroup")
	}
}
