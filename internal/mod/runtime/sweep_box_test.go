//go:build boxsweep

package runtime

// A dry run of the image sweep against a real box, without deploying the
// box: build this test binary for the box, copy the box's state database
// aside and run it there as root. It only lists (nerdctl image ls, ps) and
// reads the copy; it removes nothing. It also prints the disk report's
// runtime part.
//
//	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -tags boxsweep -o sweep.test ./internal/mod/runtime
//	scp sweep.test root@box:/root/ && ssh root@box 'cp /var/lib/tiffin/platform/state.db* /root/sweep-state/ &&
//	  TIFFIN_SWEEP_STATE=/root/sweep-state/state.db /root/sweep.test -test.run TestBoxSweepDryRun -test.v'

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

func TestBoxSweepDryRun(t *testing.T) {
	path := os.Getenv("TIFFIN_SWEEP_STATE")
	if path == "" {
		t.Skip("TIFFIN_SWEEP_STATE: a copy of the box's state.db")
	}
	db, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p := &platform.Platform{DB: db, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	opt := defaultOptions()
	r := &rt{p: p, opt: opt, st: store{db: db}, eng: newNerdctl(), ctx: ctx, locks: map[string]*sync.Mutex{},
		ports: map[int]string{}, leftover: map[string]bool{}, lastSeen: map[string]time.Time{}, timeouts: map[string]time.Duration{}}
	res, err := r.sweepImages(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	rd, err := (&Module{r: r}).DiskUse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = enc.Encode(rd)
}
