package cli

// Upgrading a Hetzner box in place: `tiffin up --name <box> --type <type>`
// changes its server type (a restart of about 2 minutes) and
// `--volume-size <GB>` grows its data volume (no downtime).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/provider"
	"github.com/shiptiffin/tiffin/internal/provider/hetzner"
)

// resizeCommand is the command that makes the resize, for hints.
func resizeCommand(name string, r *hetzner.Resize) string {
	s := "tiffin up --name " + name
	if r.To != nil {
		s += " --type " + r.To.Name
	}
	if r.VolumeToGB != r.VolumeFromGB {
		s += fmt.Sprintf(" --volume-size %d", r.VolumeToGB)
	}
	return s
}

// resizeErr maps a refused or failed plan to its exit code.
func resizeErr(err error) error {
	var ref *hetzner.Refusal
	if errors.As(err, &ref) {
		return &exitError{ExitInvalid, err.Error()}
	}
	return &exitError{ExitError, err.Error()}
}

func (a *app) printResize(name string, r *hetzner.Resize) {
	w := a.io.Out
	fmt.Fprintf(w, "Resize %s %s\n", name, a.paint("(Hetzner · "+r.Location+")", dim))
	if r.To != nil {
		fmt.Fprintf(w, "  %-8s %s → %s\n", "server", r.From.Words(), a.paint(r.To.Words(), bold))
	}
	if r.VolumeToGB != r.VolumeFromGB {
		fmt.Fprintf(w, "  %-8s %d GB → %s\n", "volume", r.VolumeFromGB, a.paint(fmt.Sprintf("%d GB", r.VolumeToGB), bold))
	}
	fmt.Fprintln(w, "\nIt will:")
	for _, s := range r.Steps {
		fmt.Fprintf(w, "  %s %s\n", a.paint("~", amber), s)
	}
	fmt.Fprintf(w, "\nMonthly price from Hetzner (%s, before %s%% VAT): %.2f → %s %s\n", r.Currency, strings.TrimRight(strings.TrimRight(r.VATRate, "0"), "."),
		r.MonthlyNetBefore, a.paint(fmt.Sprintf("%.2f", r.MonthlyNetAfter), bold), a.paint(fmt.Sprintf("(%+.2f; %.2f with VAT)", r.MonthlyNetAfter-r.MonthlyNetBefore, r.MonthlyGrossAfter), dim))
}

// canAsk reports whether a person is at the terminal to answer a question.
func (a *app) canAsk() bool {
	if !a.tty() {
		return false
	}
	f, ok := a.io.In.(*os.File)
	if !ok {
		return a.io.TTY != nil // tests hand in the answer
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// confirmResize asks before resizing: at a terminal it shows the plan and
// waits for "yes"; otherwise (agents, scripts) it reports the plan as
// confirm_required and exits 4, like every other confirmable step.
func (a *app) confirmResize(name string, r *hetzner.Resize) bool {
	hint := resizeCommand(name, r) + " --yes"
	detail := "resizing " + name + " changes what Hetzner bills"
	if r.Downtime {
		detail += " and restarts the box for about 2 minutes"
	}
	if r.VolumeToGB != r.VolumeFromGB {
		detail += "; a grown volume cannot shrink again"
	}
	if !a.tty() {
		writeJSON(a.io.Out, map[string]any{"code": "confirm_required", "status": 428, "detail": detail, "hint": "re-run with --yes: " + hint, "plan": r})
		a.code = ExitConfirm
		return false
	}
	a.printResize(name, r)
	if a.canAsk() {
		fmt.Fprintf(a.io.Out, "\nType yes to go ahead: ")
		line, _ := bufio.NewReader(a.io.In).ReadString('\n')
		if strings.EqualFold(strings.TrimSpace(line), "yes") {
			return true
		}
		fmt.Fprintln(a.io.Out, "Nothing was changed.")
	} else {
		fmt.Fprintf(a.io.Out, "\n%s nothing was changed. To go ahead: %s\n", a.paint("→", amber), a.paint(hint, bold))
	}
	a.code = ExitConfirm
	return false
}

// resizeBox makes the planned resize before the usual converge: the volume
// grows online (its filesystem grows when up prepares the data disk); a new
// server type stops Tiffin so apps finish their requests, shuts the server
// down, changes it and starts it again. Postgres, Valkey and the app pool
// are retuned by the converge that follows.
func (a *app) resizeBox(ctx context.Context, hp *hetzner.Provider, r *hetzner.Resize, reach func() (provider.Machine, error)) error {
	if r.VolumeToGB > r.VolumeFromGB {
		if err := hp.GrowVolume(ctx, r.VolumeToGB, a.progress); err != nil {
			return err
		}
	}
	if r.To == nil {
		return nil
	}
	m, err := reach()
	if err != nil {
		return err
	}
	a.progress("stopping Tiffin (apps finish their requests)")
	if _, stderr, err := m.Exec(ctx, "sudo systemctl stop tiffin; sync"); err != nil {
		return fmt.Errorf("stop Tiffin: %w\n%s", err, stderr)
	}
	if err := hp.ChangeType(ctx, r.To.Name, a.progress); err != nil {
		// The server is started again either way; if it never went down,
		// start Tiffin again too.
		_, _, _ = m.Exec(ctx, "sudo systemctl start tiffin")
		return err
	}
	return nil
}

// machineInfo is what Settings › This box shows about a Hetzner box's size
// (nil when Hetzner could not be asked: the dashboard then shows less).
func machineInfo(ctx context.Context, hp *hetzner.Provider) *platform.ServerMachine {
	m, err := hp.Machine(ctx, 4)
	if err != nil {
		return nil
	}
	raw, _ := json.Marshal(m)
	var out platform.ServerMachine
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return &out
}
