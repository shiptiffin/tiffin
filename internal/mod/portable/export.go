package portable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/mod/backup"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/mod/valkey"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// exportRun tracks one export's progress and saves it (throttled).
type exportRun struct {
	m    *Module
	p    *platform.Platform
	mu   sync.Mutex
	rec  *Export
	last time.Time
	aw   *boxfile.Writer
}

func (x *exportRun) set(f func(e *Export), force bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	f(x.rec)
	if x.aw != nil {
		x.rec.WrittenBytes = x.aw.Written()
	}
	if x.rec.EstimatedBytes > 0 {
		x.rec.Percent = int(min(99, x.rec.ContentBytes*100/x.rec.EstimatedBytes))
	}
	if force || time.Since(x.last) > time.Second {
		x.last = time.Now()
		cp := *x.rec
		_ = x.m.save(x.p, "exports", cp.ID, &cp)
	}
}

func (x *exportRun) phase(s string) { x.set(func(e *Export) { e.Phase = s }, true) }

// keyRecipient returns the age public key of the box key.
func keyRecipient(home string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(home, "secrets.key"))
	if err != nil {
		return "", err
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(raw)))
	if err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}

// keyInfo describes the box key for an export.
func keyInfo(p *platform.Platform, included bool) KeyInfo {
	loc := filepath.Join(p.Home, "secrets.key")
	k := KeyInfo{Included: included, Location: loc}
	k.Recipient, _ = keyRecipient(p.Home)
	if included {
		k.Note = "The box key is inside the archive: anyone with the file can read every secret. Keep it as safe as the box itself."
	} else {
		k.Note = "Secrets stay encrypted to this box's key, which is NOT in the archive. Importing needs it: copy " + loc +
			" off the box (as root) before you delete the box, and pass it to `tiffin box import --key-file <file>`."
	}
	return k
}

// runExport writes the archive to out. The caller owns rec's status.
func (m *Module) runExport(ctx context.Context, p *platform.Platform, rec *Export, out io.Writer) (err error) {
	x := &exportRun{m: m, p: p, rec: rec}
	start := time.Now()
	x.set(func(e *Export) { e.Status, e.StartedAt, e.Error, e.Hint = ExportRunning, now(), "", "" }, true)
	release, err := exclusive(ctx, 10*time.Minute, func() { x.phase("waiting for a running backup to finish") })
	if err != nil {
		return fmt.Errorf("%w (exports, imports, backups and restores run one at a time)", err)
	}
	defer release()

	// ---- what is on the box ----
	x.phase("looking at what is on the box")
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	meta, err := readPGMeta(ctx)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	imgs, err := liveImages(ctx, p.DB)
	if err != nil {
		return err
	}
	imgs = presentImages(ctx, imgs)
	var chosen []fileSet
	var estimate int64
	for _, s := range sets() {
		if s.History && !rec.WithHistory {
			continue
		}
		if _, err := os.Stat(s.Path(p)); err != nil {
			continue
		}
		chosen = append(chosen, s)
		estimate += datakit.DirSize(s.Path(p))
	}
	for _, d := range meta.Databases {
		estimate += d.SizeBytes
	}
	if fi, err := os.Stat(filepath.Join(valkey.DataDir, "dump.rdb")); err == nil {
		estimate += fi.Size()
	}
	x.set(func(e *Export) { e.EstimatedBytes, e.Projects = estimate, projects }, true)

	stage := filepath.Join(dir(p), "staging", rec.ID)
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	reflink := canReflink(ctx, stage)

	// ---- the snapshot: a short pause of every writer ----
	x.phase("pausing app writes for a consistent snapshot")
	pausedAt := time.Now()
	var notes []string
	nApps, resume, perr := pauseApps(ctx)
	if perr != nil {
		notes = append(notes, "app containers could not be paused ("+perr.Error()+"); databases are still each consistent")
	}
	thaw, ferr := freezeUnit(ctx, "tiffin-storage.service")
	if ferr != nil {
		notes = append(notes, "the object store could not be frozen; objects were copied live")
	}
	unpause := func() {
		thaw()
		resume()
	}
	snaps, err := exportSnapshots(ctx, meta.Databases)
	if err != nil {
		unpause()
		return fmt.Errorf("postgres snapshot: %w", err)
	}
	defer snaps.close()
	waitSave, err := bgsave(ctx)
	if err != nil {
		unpause()
		return fmt.Errorf("valkey snapshot: %w", err)
	}
	staged := map[string]string{}
	if reflink {
		for _, s := range chosen {
			if s.Live {
				continue
			}
			dst := filepath.Join(stage, "files", s.Name)
			_ = os.MkdirAll(filepath.Dir(dst), 0o700)
			if err := reflinkCopy(ctx, s.Path(p), dst); err != nil {
				_ = os.RemoveAll(dst)
				notes = append(notes, s.Name+" was read live (reflink copy failed)")
				continue
			}
			staged[s.Name] = dst
		}
	} else {
		notes = append(notes, "this disk cannot share extents (no reflinks), so file trees were read live after the pause")
	}
	stateSnap := filepath.Join(stage, "state.db")
	_, serr := p.DB.SQL().ExecContext(ctx, `VACUUM INTO ?`, stateSnap)
	unpause()
	paused := time.Since(pausedAt)
	if serr != nil {
		return fmt.Errorf("platform state: %w", serr)
	}
	x.set(func(e *Export) { e.WritesPausedMs = paused.Milliseconds() }, true)
	p.Log.Info("export: snapshot taken", "export", rec.ID, "pausedMs", paused.Milliseconds(), "apps", nApps, "reflink", reflink)

	x.phase("saving the Valkey snapshot")
	if err := waitSave(ctx); err != nil {
		return err
	}
	rdb := filepath.Join(stage, "valkey.rdb")
	if _, err := datakit.Run(ctx, "cp", "--reflink=auto", filepath.Join(valkey.DataDir, "dump.rdb"), rdb); err != nil {
		return fmt.Errorf("valkey: %w", err)
	}

	// SQLite databases of Tiffin's own modules: one consistent copy each.
	x.phase("copying SQLite databases")
	replace := map[string]map[string]string{}
	for _, s := range chosen {
		if !s.SQLite {
			continue
		}
		live := s.Path(p)
		for _, rel := range datakit.FindSQLite(live, func(rel string) bool { return s.Skip != nil && s.Skip(rel, nil, rec.WithHistory) }) {
			dst := filepath.Join(stage, "sqlite", s.Name, filepath.FromSlash(rel))
			_ = os.MkdirAll(filepath.Dir(dst), 0o700)
			if err := datakit.SQLiteSnapshot(ctx, filepath.Join(live, filepath.FromSlash(rel)), dst); err != nil {
				return fmt.Errorf("%s: snapshot %s: %w", s.Name, rel, err)
			}
			if replace[s.Name] == nil {
				replace[s.Name] = map[string]string{}
			}
			replace[s.Name][rel] = dst
		}
	}

	// ---- the archive ----
	recipient, err := keyRecipient(p.Home)
	if err != nil {
		return fmt.Errorf("box key: %w", err)
	}
	hostname, _ := os.Hostname()
	ca, _ := os.ReadFile(filepath.Join(p.Home, "ca.crt"))
	man := &boxfile.Manifest{Kind: boxfile.ManifestKind, Format: boxfile.FormatVersion, TiffinVersion: p.Version, Schema: state.SchemaVersion(),
		CreatedAt: time.Now().UTC(), Source: boxfile.Source{Domain: p.Domain, PublicURL: p.PublicURL, Hostname: hostname},
		Projects: projects, IncludesKey: rec.IncludeKey, Recipient: recipient, WithHistory: rec.WithHistory, CAPEM: string(ca),
		Consistency: boxfile.Consistency{WritesPausedMs: paused.Milliseconds(), AppsPaused: nApps, Staged: reflink && len(notes) == 0,
			Note: strings.Join(notes, "; ")}}
	if man.Projects == nil {
		man.Projects = []string{}
	}
	for _, d := range meta.Databases {
		man.Databases = append(man.Databases, d.Name)
	}
	for _, im := range imgs {
		man.Images = append(man.Images, im.Ref)
	}
	man.Parts = []string{"platform"}
	for _, s := range chosen {
		man.Parts = append(man.Parts, s.prefix())
	}
	if len(imgs) > 0 {
		man.Parts = append(man.Parts, "runtime/images")
	}
	man.Parts = append(man.Parts, "valkey", "postgres")

	aw, err := boxfile.NewWriter(out, man)
	if err != nil {
		return err
	}
	x.mu.Lock()
	x.aw = aw
	x.mu.Unlock()
	defer aw.Abort()
	aw.Progress = func(n int64) { x.set(func(e *Export) { e.ContentBytes += n }, false) }

	x.phase("writing the platform state")
	n, err := aw.WriteFile("platform/state.db", stateSnap)
	if err != nil {
		return fmt.Errorf("platform state: %w", err)
	}
	pst := boxfile.Stats{Files: 1, Bytes: n}
	if rec.IncludeKey {
		kn, err := aw.WriteFile("platform/secrets.key", filepath.Join(p.Home, "secrets.key"))
		if err != nil {
			return fmt.Errorf("box key: %w", err)
		}
		pst.Files++
		pst.Bytes += kn
	}
	aw.Part("platform", pst)

	for _, s := range chosen {
		x.phase("writing " + s.Detail)
		root := s.Path(p)
		if st, ok := staged[s.Name]; ok {
			root = st
		}
		st, err := aw.WriteTree(s.prefix(), root, boxfile.TreeOptions{Skip: s.skip(rec.WithHistory, replace[s.Name]), Replace: replace[s.Name]})
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
		aw.Part(s.Name, st)
	}

	if len(imgs) > 0 {
		x.phase(fmt.Sprintf("saving %d app image(s)", len(imgs)))
		refs := make([]string, len(imgs))
		for i, im := range imgs {
			refs[i] = im.Ref
		}
		var n int64
		err := saveImages(ctx, refs, func(r io.Reader) error {
			var err error
			n, err = aw.WriteStream("runtime/images.tar", r)
			return err
		})
		if err != nil {
			return fmt.Errorf("app images: %w", err)
		}
		aw.Part("images", boxfile.Stats{Bytes: n, Items: len(imgs)})
	}

	x.phase("writing the Valkey snapshot")
	n, err = aw.WriteFile("valkey/dump.rdb", rdb)
	if err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	aw.Part("valkey", boxfile.Stats{Files: 1, Bytes: n, Items: valkeyKeys(ctx)})

	x.phase("dumping Postgres roles")
	if err := aw.WriteJSON("postgres/meta.json", meta); err != nil {
		return err
	}
	roles, err := dumpRoles(ctx)
	if err != nil {
		return err
	}
	if err := aw.WriteBytes("postgres/roles.sql", roles, 0o600); err != nil {
		return err
	}
	var pgBytes int64
	for _, d := range meta.Databases {
		x.phase("dumping database " + d.Name)
		err := dumpDatabase(ctx, d.Name, snaps.ids[d.Name], func(r io.Reader) error {
			n, err := aw.WriteStream("postgres/db/"+d.Name+".sql", r)
			pgBytes += n
			return err
		})
		if err != nil {
			return err
		}
	}
	snaps.close()
	aw.Part("postgres", boxfile.Stats{Bytes: pgBytes, Items: len(meta.Databases)})

	x.phase("finishing")
	size, sum, tr, err := aw.Close()
	if err != nil {
		return err
	}
	x.set(func(e *Export) {
		e.Status, e.Phase, e.Percent = ExportDone, "", 100
		e.SizeBytes, e.SHA256, e.Parts, e.FinishedAt = size, sum, tr.Parts, now()
		e.WrittenBytes, e.DurationMs = size, time.Since(start).Milliseconds()
		e.Key = keyInfo(p, rec.IncludeKey)
	}, true)
	p.Log.Info("export: done", "export", rec.ID, "bytes", size, "sha256", sum, "ms", time.Since(start).Milliseconds())
	return nil
}

// failExport records a failed export.
func (m *Module) failExport(p *platform.Platform, rec *Export, err error) {
	rec.Status, rec.Phase, rec.FinishedAt = ExportFailed, "", now()
	rec.Error = err.Error()
	switch {
	case errors.Is(err, backup.ErrBusy):
		rec.Hint = "Wait for the running backup, restore, export or import to finish, then start a new export."
	case errors.Is(err, context.Canceled):
		rec.Error = "cancelled: the download stopped before the archive was complete"
		rec.Hint = "Start a new export and keep the download running until it ends."
	default:
		rec.Hint = "Start a new export; if it fails again, check `journalctl -u tiffin` on the box."
	}
	_ = m.save(p, "exports", rec.ID, rec)
	p.Log.Error("export failed", "export", rec.ID, "err", err)
}

// storeExport runs a stored-mode export into its file on the box.
func (m *Module) storeExport(p *platform.Platform, rec *Export) {
	ctx, cancel := context.WithCancel(m.boxCtx())
	m.setCancel(rec.ID, cancel)
	go func() {
		defer cancel()
		defer m.setCancel(rec.ID, nil)
		path := archivePath(p, "exports", rec.ID)
		f, err := os.OpenFile(path+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			m.failExport(p, rec, err)
			return
		}
		err = m.runExport(ctx, p, rec, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(path+".part", path)
		}
		if err != nil {
			_ = os.Remove(path + ".part")
			m.failExport(p, rec, err)
		}
	}()
}
