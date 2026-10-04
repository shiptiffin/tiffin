package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/state"
)

// Secrets stores per-project secret env vars, each encrypted to the box's
// own age key (secrets.key in the platform home, 0600). Values are only
// decrypted to start apps; the API never returns them.
type Secrets struct {
	db  *state.DB
	id  *age.X25519Identity
	now func() time.Time
}

var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// ErrSecretName is returned for names that are not UPPER_SNAKE env names.
var ErrSecretName = errors.New("secret names are UPPER_SNAKE_CASE env var names")

// OpenSecrets loads (creating on first use) the box key in home.
func OpenSecrets(db *state.DB, home string) (*Secrets, error) {
	path := filepath.Join(home, "secrets.key")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
			return nil, err
		}
		raw = []byte(id.String())
	} else if err != nil {
		return nil, err
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("secrets.key: %w", err)
	}
	return &Secrets{db: db, id: id, now: time.Now}, nil
}

// Seal encrypts a value to the box's key, for modules that keep their own
// credentials (a DNS provider token, say) outside project secrets.
func (s *Secrets) Seal(plain []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, s.id.Recipient())
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unseal decrypts what Seal produced.
func (s *Secrets) Unseal(ct []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ct), s.id)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// SecretInfo is a secret's metadata. Values are never listed.
type SecretInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// Set encrypts and stores a secret.
func (s *Secrets) Set(ctx context.Context, project, name, value, by string) error {
	if !secretName.MatchString(name) {
		return ErrSecretName
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, s.id.Recipient())
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, value); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_, err = s.db.SQL().ExecContext(ctx, `INSERT INTO secrets(project, name, ciphertext, updated_at, updated_by) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project, name) DO UPDATE SET ciphertext = excluded.ciphertext, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		project, name, buf.Bytes(), s.now().UTC().Format(time.RFC3339Nano), by)
	return err
}

// Delete removes a secret. It reports whether it existed.
func (s *Secrets) Delete(ctx context.Context, project, name string) (bool, error) {
	res, err := s.db.SQL().ExecContext(ctx, `DELETE FROM secrets WHERE project = ? AND name = ?`, project, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// List returns a project's secret names.
func (s *Secrets) List(ctx context.Context, project string) ([]SecretInfo, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT name, updated_at, updated_by FROM secrets WHERE project = ? ORDER BY name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretInfo{}
	for rows.Next() {
		var i SecretInfo
		var at string
		if err := rows.Scan(&i.Name, &at, &i.UpdatedBy); err != nil {
			return nil, err
		}
		i.UpdatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, i)
	}
	return out, rows.Err()
}

// All decrypts every secret of a project, for starting its apps.
func (s *Secrets) All(ctx context.Context, project string) (map[string]string, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT name, ciphertext FROM secrets WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name string
		var ct []byte
		if err := rows.Scan(&name, &ct); err != nil {
			return nil, err
		}
		r, err := age.Decrypt(bytes.NewReader(ct), s.id)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", name, err)
		}
		v, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		out[name] = string(v)
	}
	return out, rows.Err()
}
