package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LoadImage reads what nerdctl load says on stdout and on stderr into one
// buffer: exec copies the two streams on goroutines of their own, so they
// must share one writer (go test -race catches two).
func TestLoadImageCapturesBothStreams(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "nerdctl")
	script := `#!/bin/sh
case "$3" in
load)
  cat >/dev/null
  i=0
  while [ $i -lt 200 ]; do echo "progress $i" >&2; echo "layer $i"; i=$((i+1)); done
  echo "Loaded image: docker.io/tiffin/shop-api:dep_1"
  ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The build log is a file (safe to write from two goroutines).
	log, err := os.Create(filepath.Join(t.TempDir(), "build.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	n := &nerdctl{bin: bin}
	if err := n.LoadImage(context.Background(), strings.NewReader("tarball"), "docker.io/tiffin/shop-api:dep_1", log); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log.Name())
	if !strings.Contains(string(b), "progress 199") || !strings.Contains(string(b), "layer 199") {
		t.Fatalf("log misses lines: %q", b)
	}
}
