package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Parse decodes raw manifest JSON, validates it and applies defaults.
//
// The raw document is checked against the JSON Schema first (so explicitly
// written bad values such as "instances": 0 are caught before defaults mask
// them), then strictly decoded (unknown fields are an error), normalized and
// finally checked against the semantic rules. Any failure is a
// *ValidationError listing every problem found.
func Parse(raw []byte) (*Manifest, error) {
	m, _, err := ParseOnBox(raw, nil)
	return m, err
}

// OnBox returns each app's routes on the box now for a project (nil when
// the box has no such project).
type OnBox func(project string) (map[string][]string, error)

// ParseOnBox is Parse for a manifest about to be planned on a box: apps that
// set no routes keep the address a default already gave them there (see
// NormalizeOnBox, which also says what older lists). onBox may be nil.
func ParseOnBox(raw []byte, onBox OnBox) (m *Manifest, older []string, err error) {
	m, err = decode(raw)
	if err != nil {
		return nil, nil, err
	}
	var have map[string][]string
	if onBox != nil {
		if have, err = onBox(m.Project); err != nil {
			return nil, nil, err
		}
	}
	older = NormalizeOnBox(m, have)
	if err := Validate(m); err != nil {
		return nil, nil, err
	}
	return m, older, nil
}

// decode checks raw against the schema and decodes it strictly.
func decode(raw []byte) (*Manifest, error) {
	doc, err := decodeAny(raw)
	if err != nil {
		return nil, newValidationError([]FieldError{{Message: "invalid JSON: " + err.Error()}})
	}
	aliasErrs, renamed := serviceAliases(doc)
	if renamed {
		if raw, err = json.Marshal(doc); err != nil {
			return nil, err
		}
	}
	errs, err := validateSchema(doc)
	if err != nil {
		return nil, err
	}
	errs = append(errs, aliasErrs...)
	errs = append(errs, keyErrors(doc)...)
	if len(errs) > 0 {
		serviceHints(doc, errs)
		return nil, newValidationError(errs)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		// Reaching here means the schema and the Go types disagree.
		return nil, newValidationError([]FieldError{{Message: err.Error()}})
	}
	return &m, nil
}

// Canonical renders m as deterministic JSON: object keys sorted, no HTML
// escaping, two-space indent and a trailing newline.
func Canonical(m *Manifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("manifest: canonical: %w", err)
	}
	return buf.Bytes(), nil
}

// Load evaluates the config file at path (with env available as process.env),
// parses and validates it, and returns the manifest with its canonical JSON.
func Load(path string, env map[string]string) (*Manifest, []byte, error) {
	raw, err := EvaluateJSON(path, env)
	if err != nil {
		return nil, nil, err
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	canon, err := Canonical(m)
	if err != nil {
		return nil, nil, err
	}
	return m, canon, nil
}

// ServiceAliases are the dashboard's names for services, accepted in a
// manifest and stored under the canonical name (so `tiffin pull` writes
// the canonical one).
var ServiceAliases = map[string]string{"database": "postgres", "cache": "valkey", "files": "storage"}

// serviceAliases renames services.database/cache/files to their canonical
// names in a decoded document. It reports whether it renamed anything.
func serviceAliases(doc any) ([]FieldError, bool) {
	root, _ := doc.(map[string]any)
	svcs, _ := root["services"].(map[string]any)
	var errs []FieldError
	renamed := false
	for _, alias := range sortedKeys(ServiceAliases) {
		v, ok := svcs[alias]
		if !ok {
			continue
		}
		name := ServiceAliases[alias]
		if _, both := svcs[name]; both {
			errs = append(errs, FieldError{Path: "/services/" + alias, Message: fmt.Sprintf("%q is another name for %q; use only one of them", alias, name)})
			delete(svcs, alias)
			continue
		}
		delete(svcs, alias)
		svcs[name] = v
		renamed = true
	}
	return errs, renamed
}

// serviceGuesses maps names people try for services to what to write.
var serviceGuesses = map[string]string{
	"db": "postgres", "pg": "postgres", "postgresql": "postgres", "sql": "postgres",
	"redis": "valkey", "kv": "valkey",
	"s3": "storage", "bucket": "storage", "buckets": "storage", "uploads": "storage",
	"mail": "email", "smtp": "email",
	"signin": "auth", "login": "auth", "users": "auth",
	"jobs":  "the top-level `queues` and `crons` (not a service)",
	"queue": "the top-level `queues` (not a service)", "queues": "the top-level `queues` (not a service)",
	"cron": "the top-level `crons` (not a service)", "crons": "the top-level `crons` (not a service)",
}

// serviceHints adds what to write instead to an unknown-service error.
func serviceHints(doc any, errs []FieldError) {
	root, _ := doc.(map[string]any)
	svcs, _ := root["services"].(map[string]any)
	var tips []string
	for _, k := range sortedKeys(svcs) {
		if g, ok := serviceGuesses[strings.ToLower(k)]; ok {
			tips = append(tips, fmt.Sprintf("for %q use %s", k, g))
		}
	}
	if len(tips) == 0 {
		return
	}
	for i := range errs {
		if errs[i].Path == "/services" && strings.HasPrefix(errs[i].Message, "unknown field") {
			errs[i].Message += " (" + strings.Join(tips, "; ") + ")"
		}
	}
}
