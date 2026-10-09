package platform

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/state"
)

// Secrets stores secret env vars, each encrypted to the box's own age key
// (secrets.key in the platform home, 0600). Values are only decrypted to
// start apps; the API never returns them.
//
// A project's secrets are resources ("secret/NAME" with a SecretSpec), so
// setting or deleting one is a change in History with an undo; the API
// builds those changes with SealSpec. Modules keep their own credentials
// under pseudo-projects starting with "_" ("_email"), with Set and Delete.
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

// SecretSpec is the spec of a "secret/NAME" resource: the value sealed to
// the box's key (hex), and who set it when. Changes keep it as it is, so the
// change log never holds a secret in plain text and undo can put back the
// previous value.
type SecretSpec struct {
	Sealed    string    `json:"sealed"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// SealSpec seals value into a secret resource's spec.
func (s *Secrets) SealSpec(value, by string) (json.RawMessage, error) {
	ct, err := s.Seal([]byte(value))
	if err != nil {
		return nil, err
	}
	return json.Marshal(SecretSpec{Sealed: hex.EncodeToString(ct), UpdatedAt: s.now().UTC(), UpdatedBy: by})
}

// OpenSpec decrypts a secret resource's spec.
func (s *Secrets) OpenSpec(spec json.RawMessage) (string, error) {
	var sp SecretSpec
	if err := json.Unmarshal(spec, &sp); err != nil {
		return "", err
	}
	ct, err := hex.DecodeString(sp.Sealed)
	if err != nil {
		return "", err
	}
	v, err := s.Unseal(ct)
	return string(v), err
}

// ValidSecretName reports whether name is an UPPER_SNAKE env var name.
func ValidSecretName(name string) bool { return secretName.MatchString(name) }

// modulePseudoProject reports whether project is a module's own secret
// store ("_email") rather than a project.
func modulePseudoProject(project string) bool { return strings.HasPrefix(project, "_") }

// SecretInfo is a secret's metadata. Values are never listed.
type SecretInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// Set encrypts and stores a module secret (project "_<module>"). A
// project's own secrets change through the API, as changes.
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
	if !modulePseudoProject(project) {
		res, err := s.resources(ctx, project)
		if err != nil {
			return nil, err
		}
		out := []SecretInfo{}
		for _, r := range res {
			out = append(out, SecretInfo{Name: r.name, UpdatedAt: r.spec.UpdatedAt, UpdatedBy: r.spec.UpdatedBy})
		}
		return out, nil
	}
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

// Open decrypts what Seal encrypted (the same as Unseal).
func (s *Secrets) Open(sealed []byte) ([]byte, error) { return s.Unseal(sealed) }

type secretResource struct {
	name string
	spec SecretSpec
	raw  json.RawMessage
}

// resources reads a project's secret resources, sorted by name.
func (s *Secrets) resources(ctx context.Context, project string) ([]secretResource, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT address, spec FROM resources WHERE project = ? AND address LIKE 'secret/%' ORDER BY address`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secretResource
	for rows.Next() {
		var addr, spec string
		if err := rows.Scan(&addr, &spec); err != nil {
			return nil, err
		}
		r := secretResource{name: strings.TrimPrefix(addr, "secret/"), raw: json.RawMessage(spec)}
		if err := json.Unmarshal(r.raw, &r.spec); err != nil {
			return nil, fmt.Errorf("secret %s: %w", r.name, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// All decrypts every secret of a project, for starting its apps.
func (s *Secrets) All(ctx context.Context, project string) (map[string]string, error) {
	if !modulePseudoProject(project) {
		res, err := s.resources(ctx, project)
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for _, r := range res {
			v, err := s.OpenSpec(r.raw)
			if err != nil {
				return nil, fmt.Errorf("decrypt %s: %w", r.name, err)
			}
			out[r.name] = v
		}
		return out, nil
	}
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

// ErrNoSecret is returned when deleting a secret a project does not have.
var ErrNoSecret = errors.New("no such secret")

// PlanSecrets plans a change to a project's secrets: set gives new values
// (sealed here; a value equal to the current one is left alone), del names
// secrets to delete (ErrNoSecret if one is missing). Apply the plan with
// Engine.Apply like any other.
func (p *Platform) PlanSecrets(ctx context.Context, project string, set map[string]string, del []string, by string) (*change.Plan, error) {
	if p.Secrets == nil {
		return nil, errors.New("secrets are only available on a box")
	}
	return p.Engine.PlanEdit(ctx, project, func(cur map[string]change.Resource) (map[string]change.Resource, error) {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			if !ValidSecretName(n) {
				return nil, ErrSecretName
			}
			addr := change.KindSecret + "/" + n
			if r, ok := cur[addr]; ok {
				if v, err := p.Secrets.OpenSpec(r.Spec); err == nil && v == set[n] {
					continue
				}
			}
			spec, err := p.Secrets.SealSpec(set[n], by)
			if err != nil {
				return nil, err
			}
			cur[addr] = change.Resource{Address: addr, Spec: spec}
		}
		for _, n := range del {
			addr := change.KindSecret + "/" + n
			if _, ok := cur[addr]; !ok {
				return nil, fmt.Errorf("%w: %s in %s", ErrNoSecret, n, project)
			}
			delete(cur, addr)
		}
		return cur, nil
	})
}

// SetSecrets sets a project's secrets as one change by the system (box
// code and tests; the API applies PlanSecrets with the caller's key) and
// reconciles the project. It returns nil when nothing changed.
func (p *Platform) SetSecrets(ctx context.Context, project string, set map[string]string, intent string) (*change.Change, error) {
	plan, err := p.PlanSecrets(ctx, project, set, nil, "system")
	if err != nil {
		return nil, err
	}
	c, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Intent: intent,
		Actor: change.Actor{Kind: "system", ID: "system"}})
	if err == nil && c != nil {
		p.AfterApply(c)
	}
	return c, err
}
