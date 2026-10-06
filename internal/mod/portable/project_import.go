package portable

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

// importOptions shape a project import.
type importOptions struct {
	name        string              // the new project ("" keeps the archive's name)
	intent      string              // its first History entry
	sourceKey   *age.X25519Identity // the source box's key, for secrets sealed to another box
	skipSecrets bool
	sameBox     bool   // a duplicate: images named in release.json are this box's own
	stage       string // a scratch directory
	principal   *tokens.Principal
}

// fileOwner owns what an import writes.
type fileOwner struct{ uid, gid int }

func ownerOf(fi os.FileInfo) fileOwner {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fileOwner{int(st.Uid), int(st.Gid)}
	}
	return fileOwner{os.Geteuid(), os.Getegid()}
}

// addPlain writes a tree entry the way an import may: directories and
// regular files only (no links, which could point anywhere on the box),
// without set-id bits, owned by o rather than whoever the archive names.
// Archives are not trusted: anyone who may create a project can import one.
func addPlain(ex *boxfile.Extractor, rel string, e *boxfile.Entry, o fileOwner) error {
	if e.Header.Typeflag != tar.TypeDir && e.Header.Typeflag != tar.TypeReg {
		return nil
	}
	hd := *e.Header
	hd.Mode &= 0o777
	hd.Uname, hd.Gname, hd.Uid, hd.Gid = "", "", o.uid, o.gid
	return ex.Add(rel, &hd, e.Body)
}

var headRef = regexp.MustCompile(`^(ref: refs/heads/[A-Za-z0-9._/-]{1,200}|[0-9a-f]{40}|[0-9a-f]{64})\n?$`)

// finishRepo gives an imported repository (objects, refs and HEAD only) the
// box's own config; the runtime adds its hook on the next push.
func finishRepo(dir string) error {
	head, err := os.ReadFile(filepath.Join(dir, "HEAD"))
	if err != nil || !headRef.Match(head) {
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			return err
		}
	}
	for _, d := range []string{"objects", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return err
		}
	}
	_ = os.Chmod(dir, 0o755) // made 0700 as a scratch directory
	return os.WriteFile(filepath.Join(dir, "config"), []byte("[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n[http]\n\treceivepack = true\n"), 0o644)
}

// AppResult is how an app came across.
type AppResult struct {
	App    string `json:"app"`
	Deploy string `json:"deploy,omitempty"`
	Status string `json:"status" doc:"live, failed, or none (it had no release to bring)"`
	URL    string `json:"url,omitempty" doc:"Its address in the new project"`
	Error  string `json:"error,omitempty" doc:"Why its deploy failed"`
	Hint   string `json:"hint,omitempty" doc:"What to do about it"`
}

// imported is what an import made.
type imported struct {
	project string
	change  string
	created bool // the project exists: a later failure leaves it behind
	apps    []AppResult
	missing []string // secrets left out
	notes   []string
}

type stagedApp struct {
	rel      *appRelease
	imageTar string
	siteDir  string
}

// importProject creates a new project from an archive read from r.
func importProject(ctx context.Context, p *platform.Platform, b backend, r io.Reader, o importOptions, rep reporter) (out *imported, err error) {
	ar, err := boxfile.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	defer ar.Close()
	if err := boxfile.CheckProject(&ar.Manifest, state.SchemaVersion(), p.Version); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := os.MkdirAll(o.stage, 0o700); err != nil {
		return nil, err
	}
	out = &imported{}
	var (
		info     *ProjectInfo
		name     string
		mf       *manifest.Manifest
		secrets  = map[string]json.RawMessage{}
		history  string
		prepared bool
		ex       *boxfile.Extractor
		exKey    string
		gitStage = filepath.Join(o.stage, "git")
		apps     = map[string]*stagedApp{}
		self     = fileOwner{os.Geteuid(), os.Getegid()}
		owner    = self
	)
	defer func() {
		if err != nil && prepared && !out.created {
			if derr := b.dropDatabase(context.WithoutCancel(ctx), name); derr != nil {
				p.Log.Warn("import: could not drop the database of a failed import", "project", name, "err", derr)
			}
		}
	}()
	closeEx := func() error {
		if ex == nil {
			return nil
		}
		err := ex.Close()
		ex, exKey = nil, ""
		return err
	}
	defer closeEx()
	openEx := func(key, dir string) error {
		if exKey == key {
			return nil
		}
		if err := closeEx(); err != nil {
			return err
		}
		var err error
		ex, err = boxfile.NewExtractor(dir)
		exKey = key
		return err
	}
	by := ""
	if o.principal != nil {
		by = o.principal.TokenID
	}
	create := func() error {
		if out.created {
			return nil
		}
		if info == nil {
			return fmt.Errorf("%w: project.json is missing", ErrInvalid)
		}
		rep.say("creating project "+name, 40)
		desired, notes, err := desiredResources(info, name, secrets)
		if err != nil {
			return err
		}
		out.notes = append(out.notes, notes...)
		undoHistory := func() {}
		if history != "" {
			if undoHistory, err = insertHistory(ctx, p.DB, name, history); err != nil {
				return fmt.Errorf("history: %w", err)
			}
		}
		plan, err := p.Engine.PlanEdit(ctx, name, func(map[string]change.Resource) (map[string]change.Resource, error) { return desired, nil })
		if err != nil {
			undoHistory()
			return err
		}
		req := change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Intent: o.intent}
		if o.principal != nil {
			req.Actor, req.Authorize = o.principal.Actor(), o.principal.Authorizer()
		}
		c, err := p.Engine.Apply(ctx, req)
		if err != nil {
			undoHistory()
			return err
		}
		out.created = true
		if c != nil {
			out.change = c.ID
			p.AfterApply(c)
		}
		rep.say("starting the project's services", 45)
		return b.converged(ctx, name)
	}

	for {
		e, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		entry := e.Name
		if info == nil && entry != "project.json" {
			return out, fmt.Errorf("%w: the archive does not start with project.json", ErrInvalid)
		}
		switch {
		case entry == "project.json":
			info = &ProjectInfo{}
			if err := json.NewDecoder(e.Body).Decode(info); err != nil {
				return out, fmt.Errorf("%w: project.json: %v", ErrInvalid, err)
			}
			if info.Project != ar.Manifest.Project {
				return out, fmt.Errorf("%w: project.json is for %s, the archive for %s", ErrInvalid, info.Project, ar.Manifest.Project)
			}
			if mf, err = manifest.Parse(info.Manifest); err != nil {
				return out, fmt.Errorf("%w: project.json: %v", ErrInvalid, err)
			}
			name = o.name
			if name == "" {
				name = info.Project
			}
			out.project = name
			existing, err := p.DB.ListProjects(ctx)
			if err != nil {
				return out, err
			}
			if err := checkNewName(ctx, b, existing, name); err != nil {
				return out, err
			}
			desired, _, err := desiredResources(info, name, nil)
			if err != nil {
				return out, err
			}
			if err := p.CheckPlan(ctx, name, desired); err != nil {
				return out, err
			}
		case entry == "history/changes.jsonl":
			if history, err = spool(o.stage, "history", func(w io.Writer) error { _, err := io.Copy(w, e.Body); return err }); err != nil {
				return out, err
			}
		case entry == "secrets.json":
			var ss sealedSecrets
			if err := json.NewDecoder(e.Body).Decode(&ss); err != nil {
				return out, fmt.Errorf("%w: secrets.json: %v", ErrInvalid, err)
			}
			mine, _ := keyRecipient(p.Home)
			switch {
			case o.skipSecrets:
				out.missing = sortedKeys(ss.Secrets)
			case ss.Recipient == mine:
				secrets = ss.Secrets
			case o.sourceKey != nil && o.sourceKey.Recipient().String() == ss.Recipient:
				for n, spec := range ss.Secrets {
					v, err := openSealed(spec, o.sourceKey)
					if err != nil {
						return out, fmt.Errorf("secret %s: %w", n, err)
					}
					if secrets[n], err = p.Secrets.SealSpec(v, by); err != nil {
						return out, err
					}
				}
			default:
				out.missing = sortedKeys(ss.Secrets)
			}
		case entry == ".env":
			raw, err := io.ReadAll(io.LimitReader(e.Body, 16<<20))
			if err != nil {
				return out, err
			}
			plain := parseDotenv(string(raw))
			if o.skipSecrets {
				out.missing = sortedKeys(plain)
				break
			}
			for n, v := range plain {
				if !platform.ValidSecretName(n) {
					return out, fmt.Errorf("%w: .env: %q is not a secret name", ErrInvalid, n)
				}
				if secrets[n], err = p.Secrets.SealSpec(v, by); err != nil {
					return out, err
				}
			}
		case entry == "database.sql":
			if info.Postgres == nil || mf.Services.Postgres == nil {
				continue
			}
			rep.say("loading the database", 15)
			prepared = true
			if err := b.prepareDatabase(ctx, name, mf.Services.Postgres.Extensions, info.Postgres.Extensions); err != nil {
				return out, fmt.Errorf("postgres: %w", err)
			}
			if err := b.restoreDatabase(ctx, name, e.Body); err != nil {
				return out, fmt.Errorf("postgres: %w", err)
			}
		case entry == "cache.jsonl":
			if err := create(); err != nil {
				return out, err
			}
			rep.say("loading the cache", 55)
			err := b.restoreKeys(ctx, name, func(put func(cacheEntry) error) error {
				br := bufio.NewReaderSize(e.Body, 1<<20)
				for {
					line, rerr := br.ReadBytes('\n')
					if len(bytes.TrimSpace(line)) > 0 {
						var ce cacheEntry
						if err := json.Unmarshal(line, &ce); err != nil {
							return fmt.Errorf("%w: cache.jsonl: %v", ErrInvalid, err)
						}
						if err := put(ce); err != nil {
							return err
						}
					}
					if errors.Is(rerr, io.EOF) {
						return nil
					}
					if rerr != nil {
						return rerr
					}
				}
			})
			if err != nil {
				return out, fmt.Errorf("valkey: %w", err)
			}
		case strings.HasPrefix(entry, "files/"):
			parts := strings.SplitN(entry, "/", 3)
			if len(parts) < 3 || parts[2] == "" || mf.Services.Storage == nil {
				continue // a bucket's own directory
			}
			if _, ok := mf.Services.Storage.Buckets[parts[1]]; !ok {
				continue // a bucket the project does not have
			}
			if err := create(); err != nil {
				return out, err
			}
			dir := b.bucketDir(name, parts[1])
			if exKey != "files/"+parts[1] {
				rep.say("copying bucket "+parts[1], 60)
				fi, err := os.Stat(dir)
				if err != nil || !fi.IsDir() {
					return out, fmt.Errorf("bucket %s was not created (tiffin projects get %s says why)", parts[1], name)
				}
				owner = ownerOf(fi) // the object store's
			}
			if err := openEx("files/"+parts[1], dir); err != nil {
				return out, err
			}
			if err := addPlain(ex, parts[2], e, owner); err != nil {
				return out, fmt.Errorf("%s: %w", entry, err)
			}
		case entry == "source.git" || strings.HasPrefix(entry, "source.git/"):
			rel := strings.TrimPrefix(strings.TrimPrefix(entry, "source.git"), "/")
			if err := openEx("git", gitStage); err != nil {
				return out, err
			}
			// Only the repository's data: never its config or hooks, which git runs.
			if rel == "HEAD" || rel == "packed-refs" || strings.HasPrefix(rel, "objects/") || strings.HasPrefix(rel, "refs/") {
				if err := addPlain(ex, rel, e, self); err != nil {
					return out, fmt.Errorf("%s: %w", entry, err)
				}
			}
		case strings.HasPrefix(entry, "apps/"):
			parts := strings.SplitN(entry, "/", 4)
			if len(parts) < 3 {
				continue
			}
			app := parts[1]
			if _, ok := mf.Apps[app]; !ok {
				continue
			}
			if apps[app] == nil {
				apps[app] = &stagedApp{}
			}
			sa := apps[app]
			switch {
			case parts[2] == "release.json":
				sa.rel = &appRelease{}
				if err := json.NewDecoder(e.Body).Decode(sa.rel); err != nil {
					return out, fmt.Errorf("%w: %s: %v", ErrInvalid, entry, err)
				}
			case parts[2] == "image.tar":
				rep.say("unpacking app "+app, 70)
				if sa.imageTar, err = spool(o.stage, "image", func(w io.Writer) error { _, err := io.Copy(w, e.Body); return err }); err != nil {
					return out, err
				}
			case parts[2] == "site":
				sa.siteDir = filepath.Join(o.stage, "site-"+app)
				if err := openEx("site/"+app, sa.siteDir); err != nil {
					return out, err
				}
				if len(parts) == 4 && parts[3] != "" {
					if err := addPlain(ex, parts[3], e, self); err != nil {
						return out, fmt.Errorf("%s: %w", entry, err)
					}
				}
			}
		}
	}
	if err := closeEx(); err != nil {
		return out, err
	}
	if err := create(); err != nil {
		return out, err
	}
	if fi, err := os.Stat(filepath.Join(gitStage, "HEAD")); err == nil && !fi.IsDir() {
		if dst := b.gitDir(name); dst != "" {
			if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
				if err := finishRepo(gitStage); err != nil {
					return out, fmt.Errorf("git: %w", err)
				}
				_ = os.MkdirAll(filepath.Dir(dst), 0o755)
				if err := os.Rename(gitStage, dst); err != nil {
					return out, fmt.Errorf("git: %w", err)
				}
			}
		}
	}
	if info.Postgres != nil && len(info.Postgres.Cron) > 0 && mf.Services.Postgres != nil {
		if err := b.restoreCron(ctx, name, info.Postgres.Cron); err != nil {
			return out, fmt.Errorf("pg_cron: %w", err)
		}
	}
	for _, ai := range info.Apps {
		res := AppResult{App: ai.Name, Status: "none"}
		sa := apps[ai.Name]
		if sa != nil && sa.rel != nil {
			rep.say("starting app "+ai.Name, 80)
			src := runtime.ReleaseSource{Framework: sa.rel.Framework, Commit: sa.rel.Commit, Repo: sa.rel.Repo, Dir: sa.rel.Dir, Vercel: sa.rel.Vercel,
				Note: fmt.Sprintf("%s of %s/%s, from %s", sa.rel.Deploy, info.Project, ai.Name, orDash(info.Source.Domain))}
			switch {
			case sa.siteDir != "":
				src.StaticDir = sa.siteDir
				_ = os.Chmod(sa.siteDir, 0o755) // made 0700 as a scratch directory
			case sa.imageTar != "":
				src.ImageTar = sa.imageTar
			case o.sameBox:
				src.Image = sa.rel.Image // a duplicate tags the original's image
			}
			var d *runtime.Deploy
			var rerr error
			if src.StaticDir != "" || src.ImageTar != "" || src.Image != "" {
				d, rerr = b.release(ctx, name, ai.Name, src, by)
			}
			switch {
			case d == nil && rerr == nil:
				out.notes = append(out.notes, "App "+ai.Name+"'s release was not in the archive: deploy it again.")
			case rerr != nil:
				res.Status, res.Error = runtime.StatusFailed, rerr.Error()
				var prob *api.Problem
				if errors.As(rerr, &prob) {
					res.Hint = prob.Hint
				}
			default:
				res.Deploy, res.Status, res.URL, res.Error, res.Hint = d.ID, d.Status, d.URL, d.Error, d.Hint
			}
			if sa.imageTar != "" {
				_ = os.Remove(sa.imageTar)
			}
		}
		out.apps = append(out.apps, res)
	}
	if len(out.missing) > 0 {
		out.notes = append(out.notes, fmt.Sprintf("Secrets not imported: %s. Set them with `tiffin secrets set %s <NAME> --value ...`.", strings.Join(out.missing, ", "), name))
	}
	p.ReconcileProject(name)
	return out, nil
}

// openSealed decrypts a secret resource's spec sealed to another box's key.
func openSealed(spec json.RawMessage, id *age.X25519Identity) (string, error) {
	var sp platform.SecretSpec
	if err := json.Unmarshal(spec, &sp); err != nil {
		return "", err
	}
	ct, err := hex.DecodeString(sp.Sealed)
	if err != nil {
		return "", err
	}
	r, err := age.Decrypt(bytes.NewReader(ct), id)
	if err != nil {
		return "", err
	}
	v, err := io.ReadAll(r)
	return string(v), err
}

// insertHistory adds an archive's changes (history/changes.jsonl, oldest
// first) to project's History, before the change that creates it. IDs this
// box already has are replaced. It returns a func that takes them out again.
func insertHistory(ctx context.Context, db *state.DB, project, path string) (func(), error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cs []*change.Change
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var c change.Change
		if err := dec.Decode(&c); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%w: history/changes.jsonl: %v", ErrInvalid, err)
		}
		cs = append(cs, &c)
	}
	if len(cs) == 0 {
		return func() {}, nil
	}
	tx, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var base int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM projects WHERE name = ?`, project).Scan(&base); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	newID := map[string]string{}
	for _, c := range cs {
		var n int
		_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM changes WHERE id = ?`, c.ID).Scan(&n)
		newID[c.ID] = c.ID
		if n > 0 {
			newID[c.ID] = ids.New("chg")
		}
	}
	mapID := func(id string) string {
		if id == "" {
			return ""
		}
		if n, ok := newID[id]; ok {
			return n
		}
		return id
	}
	var top int64
	var inserted []string
	for _, c := range cs {
		c.ID, c.UndoOf, c.UndoneBy = mapID(c.ID), mapID(c.UndoOf), mapID(c.UndoneBy)
		c.Project, c.Plan.Project = project, project
		c.Version += base
		c.Plan.BaseVersion += base
		top = max(top, c.Version)
		body, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		actor, _ := json.Marshal(c.Actor)
		if _, err := tx.ExecContext(ctx, `INSERT INTO changes(id, project, version, at, actor, intent, risk, hash, undo_of, undone_by, body)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, project, c.Version, c.At.UTC().Format(time.RFC3339Nano), string(actor), c.Intent,
			string(c.Plan.Risk), c.Plan.Hash, nullable(c.UndoOf), nullable(c.UndoneBy), string(body)); err != nil {
			return nil, err
		}
		inserted = append(inserted, c.ID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(name, version) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET version = excluded.version`, project, top); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return func() {
		ctx := context.WithoutCancel(ctx)
		for _, id := range inserted {
			_, _ = db.SQL().ExecContext(ctx, `DELETE FROM changes WHERE id = ?`, id)
		}
		if base == 0 {
			_, _ = db.SQL().ExecContext(ctx, `DELETE FROM projects WHERE name = ? AND NOT EXISTS (SELECT 1 FROM resources WHERE project = ?)`, project, project)
		} else {
			_, _ = db.SQL().ExecContext(ctx, `UPDATE projects SET version = ? WHERE name = ?`, base, project)
		}
	}, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
