// Package datakit holds small helpers shared by the data modules (postgres,
// valkey, backup): per-project identifiers, generated passwords and
// credentials sealed with the box's own age key.
package datakit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
)

// Ident is the database, role and key-prefix name for a project:
// "my-shop" → "p_my_shop". Project slugs never contain "_", so the mapping
// is one-to-one, and the result is a plain SQL identifier (no quoting needed).
func Ident(project string) string { return "p_" + strings.ReplaceAll(project, "-", "_") }

// Password returns a random 32-character password (letters and digits only,
// so it is safe in URLs and config files without escaping).
func Password() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func identity(p *platform.Platform) (*age.X25519Identity, error) {
	raw, err := os.ReadFile(filepath.Join(p.Home, "secrets.key"))
	if err != nil {
		return nil, fmt.Errorf("read the box key: %w", err)
	}
	return age.ParseX25519Identity(strings.TrimSpace(string(raw)))
}

// PutSecret stores value encrypted to the box key in the platform KV.
// Values stored here are never part of an app's env unless a module puts them there.
func PutSecret(ctx context.Context, p *platform.Platform, ns, key, value string) error {
	id, err := identity(p)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, id.Recipient())
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, value); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return p.DB.KVPut(ctx, ns, key, buf.Bytes())
}

// GetSecret reads a value stored with PutSecret. ok is false when absent.
func GetSecret(ctx context.Context, p *platform.Platform, ns, key string) (string, bool, error) {
	ct, ok, err := p.DB.KVGet(ctx, ns, key)
	if err != nil || !ok {
		return "", false, err
	}
	id, err := identity(p)
	if err != nil {
		return "", false, err
	}
	r, err := age.Decrypt(bytes.NewReader(ct), id)
	if err != nil {
		return "", false, fmt.Errorf("decrypt %s/%s: %w", ns, key, err)
	}
	v, err := io.ReadAll(r)
	return string(v), err == nil, err
}

// EnsureSecret returns the stored secret, generating and storing a password first if absent.
func EnsureSecret(ctx context.Context, p *platform.Platform, ns, key string) (string, error) {
	v, ok, err := GetSecret(ctx, p, ns, key)
	if err != nil {
		return "", err
	}
	if ok {
		return v, nil
	}
	v = Password()
	return v, PutSecret(ctx, p, ns, key, v)
}

// Run runs a command and returns its combined output; on failure the error
// carries the output's tail.
func Run(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		s := strings.TrimSpace(out.String())
		if len(s) > 1500 {
			s = "…" + s[len(s)-1500:]
		}
		return out.String(), fmt.Errorf("%s: %w: %s", filepath.Base(name), err, s)
	}
	return out.String(), nil
}

// TailBuffer keeps the last 8 KiB written to it: a tool's output for an
// error message, whatever the tool prints.
type TailBuffer struct{ b []byte }

func (t *TailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 8192 {
		t.b = append(t.b[:0], t.b[len(t.b)-8192:]...)
	}
	return len(p), nil
}

func (t *TailBuffer) String() string { return strings.TrimSpace(string(t.b)) }

// ErrOffBox is returned by operations that need the box's services.
var ErrOffBox = errors.New("this needs the box's data services; it only works on a box")

// DirSize returns the total apparent size of the files under path.
func DirSize(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// ConfirmProblem is a 428 answer to a destructive operation called without
// (or with a stale) confirm hash: it carries what would be overwritten and
// the hash to repeat the call with. The CLI exits 4 on it, like plan/apply.
type ConfirmProblem struct {
	*api.Problem
	Confirm string `json:"confirm" doc:"Repeat the call with this confirm value to go ahead"`
	Preview any    `json:"preview" doc:"What the operation would do and overwrite"`
}

// RequireConfirm returns nil when confirm matches (a prefix of at least 8
// characters of) the hash of key; otherwise a *ConfirmProblem carrying
// preview. key holds what must not change between the two calls (ids,
// targets, database names), not live numbers that drift.
func RequireConfirm(confirm string, key, preview any) error {
	raw, _ := json.Marshal(key)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])[:16]
	if len(confirm) >= 8 && strings.HasPrefix(hash, confirm) {
		return nil
	}
	code, detail := "confirm_required", "this overwrites data; nothing was changed. Review the preview, then repeat with confirm"
	if confirm != "" {
		code, detail = "plan_mismatch", "the confirm value does not match what this would do now; nothing was changed"
	}
	p := api.NewProblem(428, code, detail)
	p.Hint = fmt.Sprintf("review preview, then repeat the call with confirm=%q", hash)
	return &ConfirmProblem{Problem: p, Confirm: hash, Preview: preview}
}

// ---- SQLite ----

// SQLiteSnapshot writes a consistent copy of the live SQLite database src
// to dst (VACUUM INTO reads one transaction's view, WAL included; writers
// keep going). A file copy instead misses commits still in the WAL.
//
// src is opened read-only through SQLiteURI: database files live in places
// apps write (their disk folders), and a name such as
// "x?_pragma=<SQL>" spliced into a URI would run that SQL here, as root.
func SQLiteSnapshot(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite3", SQLiteURI(src, "mode=ro&_pragma=busy_timeout(10000)"))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// SQLiteURI is a file: URI that SQLite opens as exactly the file at path
// (every character that means something in a URI escaped), with query, the
// caller's own trusted parameters.
func SQLiteURI(path, query string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query}).String()
}

// IsSQLite reports whether head starts with the SQLite header.
func IsSQLite(head []byte) bool {
	return bytes.HasPrefix(head, []byte("SQLite format 3\x00"))
}

// SQLiteSide reports SQLite's side files, never copied on their own: each
// database is snapshotted whole with VACUUM INTO.
func SQLiteSide(rel string) bool {
	return strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") || strings.HasSuffix(rel, "-journal")
}

// FindSQLite lists the SQLite databases under root (relative paths; "."
// when root is itself a database file).
func FindSQLite(root string, skip func(string) bool) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if SQLiteSide(rel) || (skip != nil && skip(rel)) {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		head := make([]byte, 16)
		n, _ := io.ReadFull(f, head)
		f.Close()
		if IsSQLite(head[:n]) {
			out = append(out, rel)
		}
		return nil
	})
	return out
}
