package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Automatic updates. The box downloads and checks a signed release itself
// (internal/mod/update), then starts `tiffin self-update --staged` in a unit
// of its own (systemd-run), since the update restarts the service that
// started it. Like `tiffin up`, the new build provisions what it needs and
// writes its unit files first; the installed build then switches to it and
// rolls back if it is unhealthy. The outcome is written to UpdatesDir for
// the box to record, audit and, on failure, alert about once it runs again.

// UpdatesDir holds downloaded releases and the outcomes of staged updates.
const UpdatesDir = Home + "/updates"

// StagedResult is how a staged update ended.
type StagedResult struct {
	ID     string `json:"id"`
	Status string `json:"status"` // ok, rolled-back, failed
	Error  string `json:"error,omitempty"`
	// EdgeRestartMs is how long the edge's restart took (ports held by its
	// socket meanwhile), when the update restarted it.
	EdgeRestartMs int64     `json:"edgeRestartMs,omitempty"`
	Seconds       float64   `json:"seconds"`
	FinishedAt    time.Time `json:"finishedAt"`
}

// WriteResult records a staged update's outcome as dir/<id>.result.json.
func WriteResult(dir string, r StagedResult) error {
	raw, _ := json.Marshal(r)
	p := filepath.Join(dir, r.ID+".result.json")
	if err := os.WriteFile(p+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// OptionsFromUnit reads the options a box was installed with back from its
// tiffin.service, so a new build can write its own unit files for the same
// box without `tiffin up`.
func OptionsFromUnit(unit string) (Options, error) {
	var o Options
	var exec []string
	for _, l := range strings.Split(unit, "\n") {
		if v, ok := strings.CutPrefix(l, "ExecStart="); ok {
			exec = strings.Fields(v)
		}
	}
	flag := func(name string) string {
		for i, f := range exec {
			if f == name && i+1 < len(exec) {
				return exec[i+1]
			}
		}
		return ""
	}
	o.Domain, o.PublicIP, o.PublicIPv6 = flag("--domain"), flag("--public-ip"), flag("--public-ipv6")
	var err1, err2 error
	o.HTTPSPort, err1 = strconv.Atoi(flag("--https-port"))
	o.HTTPPort, err2 = strconv.Atoi(flag("--http-port"))
	if o.Domain == "" || err1 != nil || err2 != nil {
		return o, errors.New("tiffin.service has no --domain, --https-port and --http-port to keep")
	}
	if u, err := url.Parse(flag("--public-url")); err == nil && u.Port() != "" {
		if p, _ := strconv.Atoi(u.Port()); p != o.HTTPSPort {
			o.PublicPort = p
		}
	}
	return o, nil
}

// WriteUnits writes this build's unit files for the box tiffin.service
// describes, keeping the current ones in UnitsBackup for a rollback (as
// `tiffin up` does).
func WriteUnits(ctx context.Context) error {
	return writeUnits(ctx, systemctl, UnitDir, UnitsBackup)
}

func writeUnits(ctx context.Context, sysctl func(context.Context, ...string) error, unitDir, backup string) error {
	cur, err := os.ReadFile(filepath.Join(unitDir, Units[0]))
	if err != nil {
		return err
	}
	o, err := OptionsFromUnit(string(cur))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return err
	}
	for _, u := range Units {
		if src := filepath.Join(unitDir, u); fileExists(src) {
			if err := copyFile(src, filepath.Join(backup, u), 0o644); err != nil {
				return err
			}
		}
	}
	for u, body := range map[string]string{Units[0]: Unit(o), Units[1]: EdgeUnit(), Units[2]: EdgeSocketUnit(o)} {
		if err := os.WriteFile(filepath.Join(unitDir, u), []byte(body), 0o644); err != nil {
			return err
		}
	}
	if err := sysctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return sysctl(ctx, append([]string{"enable"}, Units...)...)
}

// RestartEdge restarts the edge so it runs the current build. Its socket
// holds the ports meanwhile: connections wait for the new edge instead of
// being refused. It fails when the edge is not running two seconds later.
func RestartEdge(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if err := systemctl(ctx, "restart", "tiffin-edge.service"); err != nil {
		return 0, err
	}
	took := time.Since(start)
	pid := func() string {
		out, _ := exec.CommandContext(ctx, "systemctl", "show", "-p", "MainPID", "--value", "tiffin-edge.service").Output()
		return strings.TrimSpace(string(out))
	}
	first := pid()
	time.Sleep(2 * time.Second)
	if p := pid(); first == "0" || p != first {
		return took, fmt.Errorf("the edge did not keep running on the new build (pid %s, then %s)", first, p)
	}
	return took, nil
}
