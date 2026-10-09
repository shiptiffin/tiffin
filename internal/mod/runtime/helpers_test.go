package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// fakeNerdctl is a nerdctl stand-in: it logs each call's arguments (one
// call per line) and runs script for `run`.
func fakeNerdctl(t *testing.T, script string) (n *nerdctl, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "nerdctl")
	body := "#!/bin/sh\necho \"$*\" >> " + log + "\n" +
		`case "$3" in run) ` + script + `;; container) echo '[{"Name":"x","State":{"Running":true}}]';; esac` + "\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &nerdctl{bin: bin}, func() string { b, _ := os.ReadFile(log); return string(b) }
}

// A helper's output is the image's own: an error keeps its last lines, not
// all of it, however much it prints.
func TestHelperOutputIsBounded(t *testing.T) {
	n, _ := fakeNerdctl(t, `head -c 50000000 /dev/zero | tr '\0' x; echo; echo the cause; exit 3`)
	err := n.CopyOut(context.Background(), "img", []string{"dist"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "the cause") || len(err.Error()) > 16<<10 {
		t.Fatalf("error (%d bytes): %.200s", len(err.Error()), err)
	}
}

// A helper cut short is removed by name: killing nerdctl leaves its
// container running. Every helper is named, labelled and has a task cap.
func TestHelperCutShortIsRemoved(t *testing.T) {
	n, calls := fakeNerdctl(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := n.SeedDirs(ctx, "img", "", []string{"data"}, t.TempDir()); err == nil {
		t.Fatal("a helper cut short succeeded")
	}
	log := calls()
	run := strings.SplitN(log, "\n", 2)[0]
	name := ""
	if f := strings.Fields(run); len(f) > 5 && f[4] == "--name" {
		name = f[5]
	}
	if !strings.HasPrefix(name, "tiffin-helper-") || !strings.Contains(run, "--label "+helperLabel+"=1") || !strings.Contains(run, "--pids-limit") {
		t.Fatalf("helper run: %s", run)
	}
	if !strings.Contains(log, "rm --force "+name) {
		t.Errorf("the helper was not removed:\n%s", log)
	}
}

// removingEngine records the containers the builder removes.
type removingEngine struct {
	*fakeEngine
	mu      sync.Mutex
	removed []string
}

func (e *removingEngine) Remove(ctx context.Context, name string, grace time.Duration) error {
	e.mu.Lock()
	e.removed = append(e.removed, name)
	e.mu.Unlock()
	return e.fakeEngine.Remove(ctx, name, grace)
}

// A static build past its deadline is removed, not left running.
func TestStaticBuildCutShortIsRemoved(t *testing.T) {
	bin, log := fakeTools(t)
	eng := &removingEngine{fakeEngine: newFakeEngine()}
	b := &boxBuilder{eng: eng, binDir: bin, staticDir: t.TempDir(), memoryMB: 512}
	req := buildReq(t, manifest.App{Framework: manifest.FrameworkStatic, Build: "sleep 30"}, map[string]string{"index.html": "x"})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := b.Build(ctx, req); err == nil {
		t.Fatal("a build cut short succeeded")
	}
	args := strings.Split(readCalls(t, log), "\n")
	name := ""
	for i, a := range args {
		if a == "--name" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	if !strings.HasPrefix(name, "tiffin-helper-") || !strings.Contains(strings.Join(args, " "), "--pids-limit") {
		t.Fatalf("static build run: %q", args)
	}
	if len(eng.removed) != 1 || eng.removed[0] != name {
		t.Errorf("removed %q, want %s", eng.removed, name)
	}
}
