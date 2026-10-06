package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// PendingImport is the last step of a box import (internal/mod/portable):
// replacements that can only happen while the tiffin service is down,
// because the service holds them open: the state database, the box key and
// the data directories its modules write. The portable module stages the
// new files on the data disk, writes this plan and restarts the service;
// `tiffin serve --box` runs it before it opens the state database.
type PendingImport struct {
	Import string `json:"import"`
	// Aside is where replaced files and directories are moved (same disk).
	Aside      string   `json:"aside"`
	StopUnits  []string `json:"stopUnits,omitempty"`
	StartUnits []string `json:"startUnits,omitempty"`
	// RestartUnits are restarted once everything is swapped in: services
	// that keep what they read in memory but must not stop for the swap
	// (the edge, whose socket holds connections while it restarts).
	RestartUnits []string      `json:"restartUnits,omitempty"`
	Swaps        []PendingSwap `json:"swaps"`
}

// PendingSwap replaces To with From. With From empty, To is only moved
// aside. Keep lists glob patterns of entries in the old To directory that
// are carried over into the new one (box-local files the archive leaves out).
type PendingSwap struct {
	From string   `json:"from,omitempty"`
	To   string   `json:"to"`
	Keep []string `json:"keep,omitempty"`
}

// PendingResult records how a pending import went.
type PendingResult struct {
	Import  string    `json:"import"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Swapped int       `json:"swapped"`
	At      time.Time `json:"at"`
}

// PendingImportPath is where the plan waits; PendingResultPath is where
// the outcome is left for the portable module to report.
func PendingImportPath(dataRoot string) string {
	return filepath.Join(dataRoot, "portable", "pending", "plan.json")
}

func PendingResultPath(dataRoot string) string {
	return filepath.Join(dataRoot, "portable", "pending-result.json")
}

// runUnit is systemctl; tests replace it.
var runUnit = func(verb, unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", verb, unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s %s: %w: %s", verb, unit, err, out)
	}
	return nil
}

type doneSwap struct {
	s     PendingSwap
	aside string
	kept  []string
}

// ApplyPendingImport runs a pending import plan, if there is one. Every
// replacement is a rename on the data disk, and a failure puts back what
// was already replaced, so the box comes up either imported or as before.
// It reports whether a plan ran.
func ApplyPendingImport(dataRoot string, log func(string)) (bool, error) {
	planPath := PendingImportPath(dataRoot)
	raw, err := os.ReadFile(planPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	var plan PendingImport
	res := PendingResult{At: time.Now().UTC()}
	if err := json.Unmarshal(raw, &plan); err != nil {
		err = fmt.Errorf("pending import plan: %w", err)
		res.Error = err.Error()
		finishPending(dataRoot, planPath, res)
		return true, err
	}
	res.Import = plan.Import
	log("import " + plan.Import + ": swapping in the imported state")
	for _, u := range plan.StopUnits {
		if err := runUnit("stop", u); err != nil {
			log("import: " + err.Error())
		}
	}
	var done []doneSwap
	var ferr error
	for i, s := range plan.Swaps {
		d, err := swap(plan.Aside, i, s)
		if err != nil {
			ferr = fmt.Errorf("replace %s: %w", s.To, err)
			break
		}
		done = append(done, d)
	}
	if ferr != nil {
		for i := len(done) - 1; i >= 0; i-- {
			if err := unswap(done[i]); err != nil {
				log("import: put back " + done[i].s.To + ": " + err.Error())
			}
		}
		res.Error = ferr.Error()
	} else {
		res.OK = true
		res.Swapped = len(done)
	}
	for _, u := range plan.StartUnits {
		if err := runUnit("start", u); err != nil {
			log("import: " + err.Error())
		}
	}
	if ferr == nil {
		for _, u := range plan.RestartUnits {
			start := time.Now()
			if err := runUnit("restart", u); err != nil {
				log("import: " + err.Error())
				continue
			}
			log(fmt.Sprintf("import: restarted %s in %d ms", u, time.Since(start).Milliseconds()))
		}
	}
	finishPending(dataRoot, planPath, res)
	if ferr != nil {
		log("import " + plan.Import + " failed, the box keeps its previous state: " + ferr.Error())
		return true, ferr
	}
	log(fmt.Sprintf("import %s: %d items swapped in", plan.Import, len(done)))
	return true, nil
}

func finishPending(dataRoot, planPath string, res PendingResult) {
	b, _ := json.MarshalIndent(res, "", "  ")
	_ = os.MkdirAll(filepath.Dir(PendingResultPath(dataRoot)), 0o700)
	_ = os.WriteFile(PendingResultPath(dataRoot), b, 0o600)
	// The plan never runs twice.
	_ = os.Rename(planPath, planPath+".done")
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func swap(asideDir string, i int, s PendingSwap) (doneSwap, error) {
	d := doneSwap{s: s}
	if s.To == "" || !filepath.IsAbs(s.To) {
		return d, fmt.Errorf("target %q is not an absolute path", s.To)
	}
	if s.From != "" && !exists(s.From) {
		return d, fmt.Errorf("staged %s is missing", s.From)
	}
	if exists(s.To) {
		if fi, err := os.Stat(s.From); s.From != "" && err == nil && fi.IsDir() && len(s.Keep) > 0 {
			entries, _ := os.ReadDir(s.To)
			for _, e := range entries {
				for _, pat := range s.Keep {
					if ok, _ := filepath.Match(pat, e.Name()); ok && !exists(filepath.Join(s.From, e.Name())) {
						if err := os.Rename(filepath.Join(s.To, e.Name()), filepath.Join(s.From, e.Name())); err != nil {
							return d, err
						}
						d.kept = append(d.kept, e.Name())
						break
					}
				}
			}
		}
		if err := os.MkdirAll(asideDir, 0o700); err != nil {
			return d, err
		}
		d.aside = filepath.Join(asideDir, strconv.Itoa(i)+"-"+filepath.Base(s.To))
		if err := os.Rename(s.To, d.aside); err != nil {
			d.aside = ""
			_ = unkeep(d)
			return d, err
		}
	} else if err := os.MkdirAll(filepath.Dir(s.To), 0o755); err != nil {
		return d, err
	}
	if s.From != "" {
		if err := os.Rename(s.From, s.To); err != nil {
			if d.aside != "" {
				_ = os.Rename(d.aside, s.To)
			}
			_ = unkeep(d)
			return d, err
		}
	}
	return d, nil
}

func unkeep(d doneSwap) error {
	var errs []error
	for _, name := range d.kept {
		errs = append(errs, os.Rename(filepath.Join(d.s.From, name), filepath.Join(d.s.To, name)))
	}
	return errors.Join(errs...)
}

func unswap(d doneSwap) error {
	if d.s.From != "" {
		if err := os.Rename(d.s.To, d.s.From); err != nil {
			return err
		}
	}
	if d.aside != "" {
		if err := os.Rename(d.aside, d.s.To); err != nil {
			return err
		}
	}
	return unkeep(d)
}
