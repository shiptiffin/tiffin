package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

// offsiteStage is where a restore from the bucket downloads a set.
var offsiteStage = Root + "/offsite-restore"

// BackupOffsiteSet is a set in the bucket.
type BackupOffsiteSet struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind" enum:"full,incremental"`
	TakenAt       time.Time `json:"takenAt"`
	CopiedAt      time.Time `json:"copiedAt"`
	Box           string    `json:"box" doc:"Hostname of the box that took it"`
	PostgresLabel string    `json:"postgresLabel"`
	Restorable    bool      `json:"restorable" doc:"Its Postgres backup is still in the bucket (pgBackRest repo2)"`
	SizeBytes     int64     `json:"sizeBytes" doc:"Postgres cluster plus the other parts"`
	Files         int64     `json:"files"`
}

// offsiteSets lists the sets in the bucket, newest first.
func offsiteSets(ctx context.Context) ([]BackupOffsiteSet, map[string]*offsiteSet, error) {
	c, s := current()
	if c == nil {
		return nil, nil, ErrOffsiteOff
	}
	v, err := openVault(c, s)
	if err != nil {
		return nil, nil, err
	}
	ids, err := v.setIDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	labels := map[string]bool{}
	info, ierr := repoInfoOf(ctx, 2)
	for _, x := range info {
		labels[x.Label] = true
	}
	out := []BackupOffsiteSet{}
	recs := map[string]*offsiteSet{}
	for i := len(ids) - 1; i >= 0; i-- {
		rec, err := v.getSet(ctx, ids[i])
		if err != nil {
			if errors.Is(err, errNoObject) {
				continue // deleted meanwhile
			}
			return nil, nil, err
		}
		recs[ids[i]] = rec
		b := rec.Backup
		label := ""
		if b.Offsite != nil {
			label = b.Offsite.PostgresLabel
		}
		size := b.Postgres.SizeBytes + b.Valkey.SizeBytes + b.Platform.SizeBytes
		for _, f := range b.Files {
			size += f.SizeBytes
		}
		out = append(out, BackupOffsiteSet{ID: b.ID, Kind: b.Kind, TakenAt: b.StartedAt, CopiedAt: rec.CopiedAt, Box: rec.Box,
			PostgresLabel: label, Restorable: label != "" && (ierr != nil || labels[label]), SizeBytes: size, Files: rec.Upload.Files})
	}
	return out, recs, nil
}

// pickOffsite finds the set to restore: id, or the newest restorable one
// for "latest".
func pickOffsite(ctx context.Context, id string) (*offsiteSet, error) {
	list, recs, err := offsiteSets(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		if (id == "latest" && s.Restorable) || s.ID == id {
			if !s.Restorable {
				return nil, fmt.Errorf("the off-box copy of %s has no Postgres backup in the bucket any more (expired)", id)
			}
			return recs[s.ID], nil
		}
	}
	if id == "latest" {
		return nil, errors.New("the bucket holds no restorable backup set yet")
	}
	return nil, os.ErrNotExist
}

// RestoreOffsite restores a set from the bucket. Its files are downloaded
// (and checked) before anything live changes. On a box with no projects no
// safety backup is taken.
func RestoreOffsite(ctx context.Context, p *platform.Platform, rec *offsiteSet, targets []string) (*BackupRestored, error) {
	// A copy or check of the destination may be finishing: wait a little.
	for i := 0; !work.TryLock(); i++ {
		if i == 60 {
			return nil, ErrBusy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	defer work.Unlock()
	if !run.TryLock() {
		return nil, ErrBusy
	}
	defer run.Unlock()
	c, s := current()
	if c == nil {
		return nil, ErrOffsiteOff
	}
	v, err := openVault(c, s)
	if err != nil {
		return nil, err
	}
	b := rec.Backup
	if err := checkTargets(ctx, p, targets); err != nil {
		return nil, err
	}
	// Download what the targets need.
	var only []string
	for _, t := range targets {
		switch t {
		case TargetValkey:
			only = append(only, "valkey.rdb")
		case TargetFiles:
			only = append(only, "files")
		case TargetPlatform:
			only = append(only, "platform")
		}
	}
	dir := filepath.Join(offsiteStage, b.ID)
	_ = os.RemoveAll(offsiteStage)
	defer os.RemoveAll(offsiteStage)
	if len(only) > 0 {
		entries, err := v.getEntries(ctx, b.ID)
		if err != nil {
			return nil, fmt.Errorf("reading the set's file list: %w", err)
		}
		if _, err := v.getTree(ctx, entries, dir, only...); err != nil {
			return nil, fmt.Errorf("downloading the set (nothing was changed): %w", err)
		}
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	label := ""
	if b.Offsite != nil {
		label = b.Offsite.PostgresLabel
	}
	projects, _ := p.DB.ListProjects(ctx)
	return restore(ctx, p, restoreFrom{b: &b, from: SourceOffsite, dir: dir, repo: 2, label: label}, targets, len(projects) > 0)
}

// stanzaSystemIDs lists the Postgres system identifiers a repository's
// stanza has backed up (none when the stanza does not exist there).
func stanzaSystemIDs(ctx context.Context, repo int) ([]string, error) {
	out, err := pgbackrest(ctx, "--repo="+strconv.Itoa(repo), "--output=json", "info")
	if err != nil {
		return nil, err
	}
	var stanzas []struct {
		Name string `json:"name"`
		DB   []struct {
			SystemID json.Number `json:"system-id"`
		} `json:"db"`
	}
	d := json.NewDecoder(strings.NewReader(out))
	d.UseNumber()
	if err := d.Decode(&stanzas); err != nil {
		return nil, fmt.Errorf("parse pgbackrest info: %w", err)
	}
	var ids []string
	for _, s := range stanzas {
		if s.Name == Stanza {
			for _, db := range s.DB {
				ids = append(ids, db.SystemID.String())
			}
		}
	}
	return ids, nil
}

// keptNamespaces are the state's kv namespaces that stay this box's when a
// backup's platform state is swapped in: its backups, drills and off-box
// destination, and its domain.
var keptNamespaces = []string{"backup", nsSets, nsDrills, nsOffsite, "box"}

// adoptState adapts a backup's state database (path, with its box key at
// keyPath) to this box before it is swapped in: this box's owner tokens keep
// working, its backups, domain and off-box destination stay (the
// destination's secrets sealed again to the incoming key), and nothing of
// the old box's running apps or resources' status is taken as fact.
func adoptState(ctx context.Context, p *platform.Platform, path, keyPath string) error {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("the backup's box key: %w", err)
	}
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
	live := p.DB

	// 1. This box's owner tokens (the CLI on the owner's computer holds one).
	rows, err := live.SQL().QueryContext(ctx, `SELECT id, name, kind, hash, scopes, projects, sponsor, created_at, expires_at, revoked_at, last_used_at, person
		FROM tokens WHERE kind = 'owner' AND revoked_at IS NULL`)
	if err != nil {
		return err
	}
	var owners [][12]any
	for rows.Next() {
		var r [12]any
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

	// 2. This box's backups, drills, off-box destination and domain.
	for _, ns := range keptNamespaces {
		if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE ns = ?`, ns); err != nil {
			return err
		}
		kv, err := live.KVList(ctx, ns)
		if err != nil {
			return err
		}
		for k, val := range kv {
			if ns == nsOffsite && k == "config" {
				if val, err = resealConfig(p, val, id.Recipient()); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv(ns, key, value) VALUES (?, ?, ?)`, ns, k, val); err != nil {
				return err
			}
		}
	}
	// The build cache's warmth is this box's fact.
	if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE ns = 'runtime' AND key LIKE 'warmed-%'`); err != nil {
		return err
	}
	rkv, err := live.KVList(ctx, "runtime")
	if err != nil {
		return err
	}
	for k, val := range rkv {
		if strings.HasPrefix(k, "warmed-") {
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv(ns, key, value) VALUES ('runtime', ?, ?)`, k, val); err != nil {
				return err
			}
		}
	}

	// 3. No app instance of the backup's box runs here.
	type kvRow struct {
		key string
		val []byte
	}
	var rt []kvRow
	rows, err = tx.QueryContext(ctx, `SELECT key, value FROM kv WHERE ns = 'runtime/state'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r kvRow
		if err := rows.Scan(&r.key, &r.val); err != nil {
			rows.Close()
			return err
		}
		rt = append(rt, r)
	}
	rows.Close()
	for _, r := range rt {
		var m map[string]any
		if json.Unmarshal(r.val, &m) != nil {
			continue
		}
		delete(m, "instances")
		delete(m, "draining")
		m["hash"] = ""
		b, _ := json.Marshal(m)
		if _, err := tx.ExecContext(ctx, `UPDATE kv SET value = ? WHERE ns = 'runtime/state' AND key = ?`, b, r.key); err != nil {
			return err
		}
	}

	// 4. Every resource converges again here.
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE resource_status SET state = 'pending', message = 'restored; converging', updated_at = ?`, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO resource_status(project, address, state, message, updated_at)
		SELECT project, address, 'pending', 'restored; converging', ? FROM resources`, at); err != nil {
		return err
	}
	var unmanaged [][2]string
	rows, err = tx.QueryContext(ctx, `SELECT project, address FROM resource_status`)
	if err != nil {
		return err
	}
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

// resealConfig seals the off-box destination's secrets to another key.
func resealConfig(p *platform.Platform, raw []byte, to age.Recipient) ([]byte, error) {
	var c OffsiteConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	plain, err := p.Secrets.Unseal(c.Sealed)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, to)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	c.Sealed = buf.Bytes()
	return json.Marshal(c)
}

// checkOffsiteFiles downloads the off-box copy of b's other parts into
// dest, every chunk checked against its ID, and checks they would restore:
// the platform state opens with this Tiffin and lists its projects, the box
// key parses, the Valkey snapshot is an RDB file, every registered file set
// is there and its SQLite databases pass a quick check.
func checkOffsiteFiles(ctx context.Context, b *Backup, dest string) (*BackupDrillOffsite, error) {
	res := &BackupDrillOffsite{Checks: []string{}}
	c, s := current()
	if c == nil {
		return res, ErrOffsiteOff
	}
	v, err := openVault(c, s)
	if err != nil {
		return res, err
	}
	entries, err := v.getEntries(ctx, b.ID)
	if err != nil {
		return res, fmt.Errorf("reading the off-box file list of %s: %w", b.ID, err)
	}
	st, err := v.getTree(ctx, entries, dest)
	res.Files, res.Bytes, res.Chunks = st.Files, st.Bytes, st.Chunks
	if err != nil {
		return res, fmt.Errorf("downloading the off-box copy of %s: %w", b.ID, err)
	}
	res.Checks = append(res.Checks, fmt.Sprintf("%s decrypted and matched their IDs", plural(st.Chunks, "chunk")))
	problem := func(s string) { res.Problems = append(res.Problems, s) }

	if db, err := state.Open(filepath.Join(dest, "platform", "state.db")); err != nil {
		problem("the platform state does not open: " + err.Error())
	} else {
		projects, err := db.ListProjects(ctx)
		db.Close()
		if err != nil {
			problem("the platform state cannot be read: " + err.Error())
		} else {
			res.Checks = append(res.Checks, "the platform state opens ("+plural(len(projects), "project")+")")
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dest, "platform", "secrets.key")); err != nil {
		problem("the box key is missing")
	} else if _, err := age.ParseX25519Identity(strings.TrimSpace(string(raw))); err != nil {
		problem("the box key does not parse")
	} else {
		res.Checks = append(res.Checks, "the box key parses")
	}
	if b.Valkey.SizeBytes > 0 || fileExists(filepath.Join(dest, "valkey.rdb")) {
		head := make([]byte, 6)
		f, err := os.Open(filepath.Join(dest, "valkey.rdb"))
		if err == nil {
			_, err = f.Read(head)
			f.Close()
		}
		if err != nil || !(bytes.HasPrefix(head, []byte("REDIS")) || bytes.HasPrefix(head, []byte("VALKEY"))) {
			problem("the Valkey snapshot is missing or is not an RDB file")
		} else {
			res.Checks = append(res.Checks, "the Valkey snapshot is an RDB file")
		}
	}
	var names []string
	for name := range b.Files {
		names = append(names, name)
	}
	slices.Sort(names)
	dbs := 0
	for _, name := range names {
		root := filepath.Join(dest, "files", name)
		if !fileExists(root) {
			problem("file set " + name + " is missing")
			continue
		}
		for _, rel := range datakit.FindSQLite(root, nil) {
			dbs++
			if err := quickCheck(ctx, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				problem(name + "/" + rel + ": " + err.Error())
			}
		}
	}
	if len(names) > 0 {
		res.Checks = append(res.Checks, fmt.Sprintf("%s there (%s), %s pass a quick check", plural(len(names), "file set"), strings.Join(names, ", "), plural(dbs, "SQLite database")))
	}
	if len(res.Problems) > 0 {
		return res, fmt.Errorf("the off-box copy of %s does not check out: %s", b.ID, strings.Join(clip(res.Problems, 5), "; "))
	}
	return res, nil
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// quickCheck runs SQLite's quick_check on a database file.
func quickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var out string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&out); err != nil {
		return err
	}
	if out != "ok" {
		return errors.New("quick_check: " + out)
	}
	return nil
}
