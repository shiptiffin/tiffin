package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Parse decodes raw manifest JSON, validates it and applies defaults.
//
// The raw document is checked against the JSON Schema first (so explicitly
// written bad values such as "instances": 0 are caught before defaults mask
// them), then strictly decoded (unknown fields are an error), normalized and
// finally checked against the semantic rules. Any failure is a
// *ValidationError listing every problem found.
func Parse(raw []byte) (*Manifest, error) {
	doc, err := decodeAny(raw)
	if err != nil {
		return nil, newValidationError([]FieldError{{Message: "invalid JSON: " + err.Error()}})
	}
	errs, err := validateSchema(doc)
	if err != nil {
		return nil, err
	}
	errs = append(errs, keyErrors(doc)...)
	if len(errs) > 0 {
		return nil, newValidationError(errs)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		// Reaching here means the schema and the Go types disagree.
		return nil, newValidationError([]FieldError{{Message: err.Error()}})
	}
	Normalize(&m)
	if err := Validate(&m); err != nil {
		return nil, err
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
