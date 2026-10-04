package portable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/mod/backup"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/jackc/pgx/v5"
)

func pendingDir(p *platform.Platform) string { return filepath.Join(dir(p), "pending") }

// ErrInvalid wraps archives that fail verification or do not fit this box.
var ErrInvalid = errors.New("invalid archive")

// receive stores an uploaded archive while verifying it in the same pass:
// manifest first, every entry against the trailer's digest, then the
// version check. Only a verified archive is kept.
func (m *Module) receive(ctx context.Context, p *platform.Platform, body io.Reader, size int64, by, version string) (*Import, error) {
	if size > 0 {
		if free := freeBytes(dir(p)); free >= 0 && free < size+512<<20 {
			return nil, fmt.Errorf("%w: not enough disk space for the upload (%s free, the archive is %s)", errNoSpace, human(free), human(size))
		}
	}
	rec := &Import{ID: ids.New("im"), Status: ImportUploaded, UploadedBy: by, UploadedAt: time.Now().UTC()}
	path := archivePath(p, "imports", rec.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(path + ".part")
		}
	}()
	h := sha256.New()
	cw := &hashCounter{w: f, h: h}
	tee := io.TeeReader(body, cw)
	man, tr, verr := boxfile.Verify(tee)
	if verr != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, verr)
	}
	// Whatever follows the tar end (zstd's last frame bytes) belongs to the file too.
	if _, err := io.Copy(cw, body); err != nil {
		return nil, fmt.Errorf("upload interrupted: %w", err)
	}
	if err := boxfile.CheckCompatible(man, state.SchemaVersion(), version); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(path+".part", path); err != nil {
		return nil, err
	}
	ok = true
	rec.SizeBytes, rec.SHA256 = cw.n, hex.EncodeToString(h.Sum(nil))
	rec.Source, rec.Parts = summarize(man), tr.Parts
	if err := m.save(p, "imports", rec.ID, rec); err != nil {
		return nil, err
	}
	p.Log.Info("import: archive received", "import", rec.ID, "bytes", rec.SizeBytes, "sha256", rec.SHA256)
	return rec, nil
}

var errNoSpace = errors.New("no space")

// hashCounter writes through to w, hashing and counting.
type hashCounter struct {
	w io.Writer
	h hash.Hash
	n int64
}

func (c *hashCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

// readManifest reads an uploaded archive's manifest.
func readManifest(p *platform.Platform, id string) (*boxfile.Manifest, error) {
	f, err := os.Open(archivePath(p, "imports", id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ar, err := boxfile.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer ar.Close()
	return &ar.Manifest, nil
}

// ImportPreview is what applying an import would do. Its hash is the
// confirm value.
type ImportPreview struct {
	Import           string   `json:"import"`
	SHA256           string   `json:"sha256"`
	Source           Summary  `json:"source"`
	Replace          bool     `json:"replace"`
	ExistingProjects []string `json:"existingProjects" doc:"Projects on this box now"`
	Overwrites       []string `json:"overwrites" doc:"What is replaced, in plain words"`
	Keeps            []string `json:"keeps" doc:"What stays as it is"`
	Downtime         string   `json:"downtime"`
	SafetyBackup     string   `json:"safetyBackup"`
}

// Key is what the confirm value covers: the archive, replace, and the
// projects that would be overwritten.
func (pv *ImportPreview) Key() any {
	return []any{"box-import", pv.Import, pv.SHA256, pv.Replace, pv.ExistingProjects}
}

func preview(ctx context.Context, p *platform.Platform, rec *Import, replace bool) (*ImportPreview, error) {
	existing, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		existing = []string{}
	}
	s := rec.Source
	pv := &ImportPreview{Import: rec.ID, SHA256: rec.SHA256, Source: s, Replace: replace, ExistingProjects: existing,
		Downtime:     "The box's API restarts once to swap in the imported state (seconds); apps then start from the archive's images and files.",
		SafetyBackup: "none: this box has no projects to lose"}
	pv.Overwrites = []string{
		fmt.Sprintf("Projects become the archive's %d project(s) (%s), with their change history, secrets, settings and deploys", len(s.Projects), strings.Join(s.Projects, ", ")),
		fmt.Sprintf("Postgres databases %s are loaded from the archive (any of the same name here are dropped first)", strings.Join(s.Databases, ", ")),
		"Valkey data is replaced by the archive's snapshot",
		"Buckets and objects, email, analytics, observe settings, static sites and git repositories are replaced by the archive's",
		"Tokens, people and passkeys become the archive's (this box's owner token is kept as well)",
		"The box key becomes the archive's key, and the HTTPS certificate authority becomes the source box's (re-trust it in browsers that trusted this box)",
	}
	pv.Keeps = []string{"This box's domain, public URL and installed Tiffin build", "This box's owner token", "This box's backups (local repository) and backup schedule"}
	if replace && len(existing) > 0 {
		pv.Overwrites = append([]string{fmt.Sprintf("Everything of this box's %d project(s) (%s) is removed: apps stopped, databases dropped, files replaced", len(existing), strings.Join(existing, ", "))}, pv.Overwrites...)
		pv.SafetyBackup = "a full backup of this box is taken first (Postgres, Valkey, platform state, files); its ID is returned"
	}
	return pv, nil
}

// ApplyOptions are the apply step's inputs.
type applyOptions struct {
	replace bool
	key     string // age identity text, when the archive does not carry its key
}

// checkKey makes sure the archive's secrets can be decrypted here.
func checkKey(s Summary, key string) error {
	if s.IncludesKey {
		return nil
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("%w: the archive's secrets are encrypted to the source box's key (%s), which is not inside it; pass that key (the contents of /var/lib/tiffin/platform/secrets.key on the source box)", errNeedKey, s.Recipient)
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(key))
	if err != nil {
		return fmt.Errorf("%w: that is not an age secret key (AGE-SECRET-KEY-1...): %v", errNeedKey, err)
	}
	if got := id.Recipient().String(); got != s.Recipient {
		return fmt.Errorf("%w: that key (%s) is not the one the archive's secrets are encrypted to (%s)", errNeedKey, got, s.Recipient)
	}
	return nil
}

var errNeedKey = errors.New("box key needed")

// importRun tracks one import's progress.
type importRun struct {
	m    *Module
	p    *platform.Platform
	mu   sync.Mutex
	rec  *Import
	last time.Time
}

func (r *importRun) set(f func(im *Import), force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r.rec)
	if force || time.Since(r.last) > time.Second {
		r.last = time.Now()
		cp := *r.rec
		_ = r.m.save(r.p, "imports", cp.ID, &cp)
	}
}

func (r *importRun) phase(s string) { r.set(func(im *Import) { im.Phase = s }, true) }

// startImport runs the restore in the background (box lifetime).
func (m *Module) startImport(p *platform.Platform, rec *Import, o applyOptions) {
	ctx, cancel := context.WithCancel(m.boxCtx())
	m.setCancel(rec.ID, cancel)
	r := &importRun{m: m, p: p, rec: rec}
	go func() {
		defer cancel()
		defer m.setCancel(rec.ID, nil)
		if err := m.runImport(ctx, r, o); err != nil {
			r.set(func(im *Import) {
				im.Status, im.Phase, im.FinishedAt = ImportFailed, "", now()
				im.Error = err.Error()
				im.Hint = restoreHint(im)
			}, true)
			p.Log.Error("import failed", "import", rec.ID, "err", err)
			_ = os.RemoveAll(pendingDir(p))
		}
	}()
}

func restoreHint(im *Import) string {
	switch {
	case !im.Touched:
		return "Nothing on this box was changed. Fix the cause and apply the import again."
	case im.SafetyBackup != "":
		return "Data on this box was partly replaced. Put it back with `tiffin restore " + im.SafetyBackup + " --targets postgres,valkey,files`, or apply the import again."
	default:
		return "Data on this box was partly replaced. Apply the import again (it starts over), or recreate the box."
	}
}

type progressReader struct {
	r io.Reader
	n int64
	f func(int64)
}

func (pr *progressReader) Read(b []byte) (int, error) {
	n, err := pr.r.Read(b)
	pr.n += int64(n)
	pr.f(pr.n)
	return n, err
}

// runImport restores the archive: data services now, platform state and
// file trees through a pending plan the restarted service swaps in.
func (m *Module) runImport(ctx context.Context, r *importRun, o applyOptions) error {
	p, rec := r.p, r.rec
	start := time.Now()
	r.set(func(im *Import) {
		im.Status, im.StartedAt, im.Error, im.Hint, im.Replace = ImportApplying, now(), "", "", o.replace
	}, true)
	man, err := readManifest(p, rec.ID)
	if err != nil {
		return err
	}
	var total int64
	for _, s := range rec.Parts {
		total += s.Bytes
	}
	if free := freeBytes(dir(p)); free >= 0 && free < total+total/10+256<<20 {
		return fmt.Errorf("not enough disk space to restore: %s free, the archive unpacks to about %s", human(free), human(total))
	}
	if o.replace {
		r.phase("taking a safety backup of this box")
		var b *backup.Backup
		var err error
		for deadline := time.Now().Add(10 * time.Minute); ; {
			b, err = backup.Take(ctx, p, "full", "pre-restore")
			if !errors.Is(err, backup.ErrBusy) || time.Now().After(deadline) {
				break
			}
			select { // a scheduled backup is running: wait for it
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		if err != nil {
			return fmt.Errorf("safety backup failed, nothing was changed: %w", err)
		}
		r.set(func(im *Import) { im.SafetyBackup = b.ID }, true)
	}
	unlock, err := exclusive(ctx, 10*time.Minute, func() { r.phase("waiting for a running backup to finish") })
	if err != nil {
		return fmt.Errorf("%w (exports, imports, backups and restores run one at a time)", err)
	}
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	pend := pendingDir(p)
	_ = os.RemoveAll(pend)
	if err := os.MkdirAll(pend, 0o700); err != nil {
		return err
	}

	f, err := os.Open(archivePath(p, "imports", rec.ID))
	if err != nil {
		return err
	}
	defer f.Close()
	pr := &progressReader{r: f, f: func(n int64) {
		r.set(func(im *Import) { im.Percent = int(min(95, n*95/max(1, rec.SizeBytes))) }, false)
	}}
	ar, err := boxfile.NewReader(pr)
	if err != nil {
		return err
	}
	defer ar.Close()
	touch := func() { r.set(func(im *Import) { im.Touched = true }, true) }

	if o.replace {
		r.phase("stopping this box's apps")
		touch()
		if _, err := removeApps(ctx); err != nil {
			return err
		}
	}

	var (
		meta  *pgMeta
		admin *pgx.Conn
		ex    *boxfile.Extractor
		exSet string
		got   = map[string]bool{}
	)
	defer func() {
		if admin != nil {
			admin.Close(context.WithoutCancel(ctx))
		}
	}()
	closeEx := func() error {
		if ex == nil {
			return nil
		}
		err := ex.Close()
		ex, exSet = nil, ""
		return err
	}
	defer closeEx()
	writeFile := func(dst string, body io.Reader, mode os.FileMode) error {
		w, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, body); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}

	for {
		e, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := e.Name
		switch {
		case name == "platform/state.db":
			r.phase("unpacking the platform state")
			if err := writeFile(filepath.Join(pend, "state.db"), e.Body, 0o600); err != nil {
				return err
			}
		case name == "platform/secrets.key":
			if err := writeFile(filepath.Join(pend, "secrets.key"), e.Body, 0o600); err != nil {
				return err
			}
		case strings.HasPrefix(name, "files/") || strings.HasPrefix(name, "history/"):
			parts := strings.SplitN(name, "/", 3)
			set := parts[1]
			if _, ok := setByName(set); !ok {
				continue // a set this build does not know: leave it
			}
			if set != exSet {
				if err := closeEx(); err != nil {
					return err
				}
				fs, _ := setByName(set)
				r.phase("unpacking " + fs.Detail)
				if ex, err = boxfile.NewExtractor(filepath.Join(pend, "files", set)); err != nil {
					return err
				}
				exSet = set
				got[set] = true
			}
			rel := ""
			if len(parts) == 3 {
				rel = parts[2]
			}
			if err := ex.Add(rel, e.Header, e.Body); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		case name == "runtime/images.tar":
			r.phase(fmt.Sprintf("loading %d app image(s)", len(man.Images)))
			touch()
			if err := loadImages(ctx, e.Body); err != nil {
				return err
			}
		case name == "valkey/dump.rdb":
			r.phase("restoring Valkey")
			rdb := filepath.Join(pend, "valkey.rdb")
			if err := writeFile(rdb, e.Body, 0o600); err != nil {
				return err
			}
			touch()
			if err := backup.RestoreValkeyRDB(ctx, rdb); err != nil {
				return fmt.Errorf("valkey: %w", err)
			}
			_ = os.Remove(rdb)
		case name == "postgres/meta.json":
			meta = &pgMeta{}
			if err := json.NewDecoder(e.Body).Decode(meta); err != nil {
				return fmt.Errorf("%w: postgres/meta.json: %v", boxfile.ErrCorrupt, err)
			}
			if admin, err = postgres.Admin(ctx, "postgres"); err != nil {
				return fmt.Errorf("connect to postgres: %w", err)
			}
			if o.replace {
				touch()
				if err := dropOtherDatabases(ctx, admin, meta); err != nil {
					return err
				}
			}
		case name == "postgres/roles.sql":
			r.phase("restoring Postgres roles")
			raw, err := io.ReadAll(e.Body)
			if err != nil {
				return err
			}
			touch()
			if err := restoreRoles(ctx, raw); err != nil {
				return err
			}
		case strings.HasPrefix(name, "postgres/db/") && e.Stream:
			db := strings.TrimSuffix(strings.TrimPrefix(name, "postgres/db/"), ".sql")
			if meta == nil || admin == nil {
				return fmt.Errorf("%w: database dump before postgres/meta.json", boxfile.ErrCorrupt)
			}
			i := slices.IndexFunc(meta.Databases, func(d pgDatabase) bool { return d.Name == db })
			if i < 0 {
				return fmt.Errorf("%w: dump of %s without its settings", boxfile.ErrCorrupt, db)
			}
			d := meta.Databases[i]
			r.phase("restoring database " + db)
			touch()
			if err := dropDatabase(ctx, admin, db); err != nil {
				return fmt.Errorf("drop %s: %w", db, err)
			}
			if err := createDatabase(ctx, admin, d); err != nil {
				return fmt.Errorf("create %s: %w", db, err)
			}
			if _, err := psql(ctx, db, e.Body, true); err != nil {
				return fmt.Errorf("restore %s: %w", db, err)
			}
			if err := finishDatabase(ctx, admin, d); err != nil {
				return fmt.Errorf("restore %s: %w", db, err)
			}
		default:
			p.Log.Warn("import: skipping an unknown archive entry", "entry", name)
		}
	}
	if err := closeEx(); err != nil {
		return err
	}
	if meta != nil && admin != nil {
		r.phase("restoring pg_cron jobs")
		if err := restoreCron(ctx, admin, meta.Cron, o.replace); err != nil {
			return err
		}
		if o.replace {
			dropLeftoverRoles(ctx, admin, meta, p)
		}
	}

	// The box key: the archive's, or the one given with the apply.
	keyPath := filepath.Join(pend, "secrets.key")
	if _, err := os.Stat(keyPath); err != nil {
		if strings.TrimSpace(o.key) == "" {
			return errors.New("the archive carries no box key and none was given")
		}
		if err := os.WriteFile(keyPath, []byte(strings.TrimSpace(o.key)+"\n"), 0o600); err != nil {
			return err
		}
	}
	r.phase("preparing the platform state")
	if err := fixState(ctx, filepath.Join(pend, "state.db"), p.DB, man); err != nil {
		return fmt.Errorf("platform state: %w", err)
	}

	plan := platform.PendingImport{Import: rec.ID, Aside: filepath.Join(dir(p), "pre-import-"+rec.ID)}
	plan.Swaps = append(plan.Swaps,
		platform.PendingSwap{From: filepath.Join(pend, "state.db"), To: filepath.Join(p.Home, "state.db")},
		platform.PendingSwap{To: filepath.Join(p.Home, "state.db-wal")},
		platform.PendingSwap{To: filepath.Join(p.Home, "state.db-shm")},
		platform.PendingSwap{From: keyPath, To: filepath.Join(p.Home, "secrets.key")})
	for _, s := range sets() {
		if !got[s.Name] {
			if o.replace && !s.History {
				plan.Swaps = append(plan.Swaps, platform.PendingSwap{To: s.Path(p)}) // the archive has none: clear this box's
			}
			continue
		}
		plan.Swaps = append(plan.Swaps, platform.PendingSwap{From: filepath.Join(pend, "files", s.Name), To: s.Path(p), Keep: s.keep(man.WithHistory)})
		for _, u := range s.units(man.WithHistory) {
			if !slices.Contains(plan.StopUnits, u) {
				plan.StopUnits = append(plan.StopUnits, u)
				plan.StartUnits = append(plan.StartUnits, u)
			}
		}
	}
	b, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(platform.PendingImportPath(p.DataRoot), b, 0o600); err != nil {
		return err
	}
	_ = os.Remove(platform.PendingResultPath(p.DataRoot))
	r.set(func(im *Import) {
		im.Status, im.Phase, im.Percent = ImportRestarting, "restarting the box's service to swap in the imported state", 96
		im.DurationMs = time.Since(start).Milliseconds()
	}, true)
	release()
	p.Log.Info("import: data restored, restarting to swap in the state", "import", rec.ID, "ms", time.Since(start).Milliseconds())
	// --no-block: systemd stops this process (SIGTERM) and starts the new one.
	if _, err := datakit.Run(context.WithoutCancel(ctx), "systemctl", "restart", "--no-block", "tiffin"); err != nil {
		_ = os.Remove(platform.PendingImportPath(p.DataRoot))
		return fmt.Errorf("restart the service: %w", err)
	}
	return nil
}

// dropOtherDatabases removes this box's databases the archive does not
// have (import --replace); the archive's are dropped as they are restored.
func dropOtherDatabases(ctx context.Context, c *pgx.Conn, meta *pgMeta) error {
	rows, err := c.Query(ctx, `SELECT datname FROM pg_database WHERE datname NOT IN ('template0', 'template1', 'postgres')`)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && !slices.ContainsFunc(meta.Databases, func(d pgDatabase) bool { return d.Name == n }) {
			drop = append(drop, n)
		}
	}
	rows.Close()
	for _, n := range drop {
		if err := dropDatabase(ctx, c, n); err != nil {
			return fmt.Errorf("drop %s: %w", n, err)
		}
	}
	return nil
}

// dropLeftoverRoles removes project roles of this box the archive does not
// have (import --replace). Failures only leave an unused role behind.
func dropLeftoverRoles(ctx context.Context, c *pgx.Conn, meta *pgMeta, p *platform.Platform) {
	rows, err := c.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname LIKE 'p\_%'`)
	if err != nil {
		return
	}
	var drop []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && !slices.Contains(meta.Roles, n) {
			drop = append(drop, n)
		}
	}
	rows.Close()
	for _, n := range drop {
		if _, err := c.Exec(ctx, `DROP OWNED BY `+qi(n)+`; DROP ROLE `+qi(n)); err != nil {
			p.Log.Warn("import: could not drop a leftover role", "role", n, "err", err)
		}
	}
}

// fixState adapts the imported platform state to this box before it is
// swapped in.
func fixState(ctx context.Context, path string, live *state.DB, man *boxfile.Manifest) error {
	db, err := state.Open(path) // also migrates an older box's schema
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. This box's owner token keeps working (the CLI on your computer
	// holds it); the archive's tokens come along too.
	rows, err := live.SQL().QueryContext(ctx, `SELECT id, name, kind, hash, scopes, projects, sponsor, created_at, expires_at, revoked_at, last_used_at, person
		FROM tokens WHERE kind = 'owner' AND revoked_at IS NULL`)
	if err != nil {
		return err
	}
	type row = [12]any
	var owners []row
	for rows.Next() {
		var r row
		ptrs := make([]any, len(r))
		for i := range r {
			ptrs[i] = &r[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return err
		}
		owners = append(owners, r)
	}
	rows.Close()
	for _, r := range owners {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tokens(id, name, kind, hash, scopes, projects, sponsor, created_at, expires_at, revoked_at, last_used_at, person)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r[:]...); err != nil {
			return fmt.Errorf("keep this box's owner token: %w", err)
		}
	}

	// 2. Backups belong to this box's own repository.
	if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE ns IN ('backup.sets', 'backup')`); err != nil {
		return err
	}
	for _, ns := range []string{"backup.sets", "backup"} {
		kv, err := live.KVList(ctx, ns)
		if err != nil {
			return err
		}
		for k, v := range kv {
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv(ns, key, value) VALUES (?, ?, ?)`, ns, k, v); err != nil {
				return err
			}
		}
	}

	// The build cache is this box's too: whether it is warm (the runtime's
	// "warmed-<versions>" marks) is this box's fact, not the archive's.
	if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE ns = 'runtime' AND key LIKE 'warmed-%'`); err != nil {
		return err
	}
	rkv, err := live.KVList(ctx, "runtime")
	if err != nil {
		return err
	}
	for k, v := range rkv {
		if strings.HasPrefix(k, "warmed-") {
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv(ns, key, value) VALUES ('runtime', ?, ?)`, k, v); err != nil {
				return err
			}
		}
	}

	// 3. Apps: no instance of the source box runs here; the runtime starts
	// fresh ones from the live deploys. Deploys whose image did not travel
	// can no longer be rolled back to.
	rows, err = tx.QueryContext(ctx, `SELECT ns, key, value FROM kv WHERE ns = ? OR ns LIKE 'runtime/deploys/%'`, nsRuntimeState)
	if err != nil {
		return err
	}
	type kvRow struct {
		ns, key string
		val     []byte
	}
	var kvs []kvRow
	for rows.Next() {
		var r kvRow
		if err := rows.Scan(&r.ns, &r.key, &r.val); err != nil {
			rows.Close()
			return err
		}
		kvs = append(kvs, r)
	}
	rows.Close()
	for _, r := range kvs {
		var v map[string]any
		if json.Unmarshal(r.val, &v) != nil {
			continue
		}
		if r.ns == nsRuntimeState {
			delete(v, "instances")
			delete(v, "draining")
			v["hash"] = ""
		} else if img, _ := v["image"].(string); img != "" && !slices.Contains(man.Images, img) {
			v["image"] = ""
		} else {
			continue
		}
		b, _ := json.Marshal(v)
		if _, err := tx.ExecContext(ctx, `UPDATE kv SET value = ? WHERE ns = ? AND key = ?`, b, r.ns, r.key); err != nil {
			return err
		}
	}

	// 4. Every resource converges again here: until it has, it is pending.
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE resource_status SET state = 'pending', message = 'imported; converging', updated_at = ?`, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO resource_status(project, address, state, message, updated_at)
		SELECT project, address, 'pending', 'imported; converging', ? FROM resources`, at); err != nil {
		return err
	}
	// ... except secrets: nothing converges them (apps read them when they
	// start), so a pending row would never settle and the import would wait
	// it out.
	rows, err = tx.QueryContext(ctx, `SELECT project, address FROM resource_status`)
	if err != nil {
		return err
	}
	var unmanaged [][2]string
	for rows.Next() {
		var pr, addr string
		if err := rows.Scan(&pr, &addr); err != nil {
			rows.Close()
			return err
		}
		if change.Unmanaged(addr) {
			unmanaged = append(unmanaged, [2]string{pr, addr})
		}
	}
	rows.Close()
	for _, u := range unmanaged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_status WHERE project = ? AND address = ?`, u[0], u[1]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.SQL().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// resume continues an import after the service restarted into it.
func (m *Module) resume(ctx context.Context, p *platform.Platform, rec *Import) {
	raw, err := os.ReadFile(platform.PendingResultPath(p.DataRoot))
	var res platform.PendingResult
	if err == nil {
		err = json.Unmarshal(raw, &res)
	}
	fail := func(msg string) {
		rec.Status, rec.Phase, rec.FinishedAt, rec.Error = ImportFailed, "", now(), msg
		rec.Hint = restoreHint(rec)
		_ = m.save(p, "imports", rec.ID, rec)
		p.Log.Error("import failed", "import", rec.ID, "err", msg)
	}
	switch {
	case err != nil:
		fail("the restarted service found no record of swapping in the imported state (" + err.Error() + ")")
		return
	case res.Import != rec.ID:
		fail("the restarted service swapped in a different import (" + res.Import + ")")
		return
	case !res.OK:
		fail("swapping in the imported state failed, so the box kept its previous state: " + res.Error)
		return
	}
	_ = os.RemoveAll(pendingDir(p))
	rec.Status, rec.Phase, rec.RestartedAt, rec.Percent = ImportConverging, "starting projects and apps", now(), 97
	_ = m.save(p, "imports", rec.ID, rec)
	m.converge(ctx, p, rec)
}

// converge waits until every project has reconciled and the box's checks
// pass (or time runs out), then records the outcome.
func (m *Module) converge(ctx context.Context, p *platform.Platform, rec *Import) {
	deadline := time.Now().Add(15 * time.Minute)
	var settled time.Time
	var failing []Check
	healthy := false
	for {
		failing = failing[:0]
		pending := 0
		projects, _ := p.DB.ListProjects(ctx)
		for _, pr := range projects {
			st, _ := p.DB.ResourceStatuses(ctx, pr)
			for addr, s := range st {
				switch s.State {
				case platform.StatePending:
					pending++
				case platform.StateFailed:
					failing = append(failing, Check{Name: pr + "/" + addr, Detail: s.Message})
				}
			}
		}
		if pending == 0 {
			if settled.IsZero() {
				settled = time.Now()
			}
			for _, c := range p.Checks(ctx) {
				if !c.OK {
					failing = append(failing, Check{Name: c.Name, Detail: c.Detail})
				}
			}
			if len(failing) == 0 {
				healthy = true
				break
			}
			// Checks may need a moment after apps start (health probes, metrics).
			if time.Since(settled) > 3*time.Minute {
				break
			}
		}
		if time.Now().After(deadline) {
			if pending > 0 {
				failing = append(failing, Check{Name: "reconcile", Detail: fmt.Sprintf("%d resource(s) still pending", pending)})
			}
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	rec.Status, rec.Phase, rec.Percent, rec.FinishedAt = ImportDone, "", 100, now()
	rec.Healthy, rec.Failing = healthy, append([]Check(nil), failing...)
	if rec.StartedAt != nil {
		rec.DurationMs = time.Since(*rec.StartedAt).Milliseconds()
	}
	if !healthy {
		rec.Hint = "The import finished but not everything is green: see failing, `tiffin status` and `tiffin projects get <project>`."
	}
	_ = m.save(p, "imports", rec.ID, rec)
	// The uploaded archive and the replaced files are no longer needed.
	_ = os.Remove(archivePath(p, "imports", rec.ID))
	if healthy {
		_ = os.RemoveAll(filepath.Join(dir(p), "pre-import-"+rec.ID))
	}
	p.Log.Info("import: done", "import", rec.ID, "healthy", healthy, "ms", rec.DurationMs)
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
