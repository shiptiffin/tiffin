package manifest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// SchemaID is the $id of the manifest JSON Schema (placeholder domain).
const SchemaID = "https://shiptiffin.com/schema/manifest-v1.json"

//go:embed schema.json
var schemaJSON []byte

// Schema returns the JSON Schema (draft 2020-12) describing manifest v1.
// Agents and editors can use it to discover every field, its default and its
// constraints. The returned slice is a copy.
func Schema() []byte {
	return bytes.Clone(schemaJSON)
}

var (
	compileOnce    sync.Once
	compiledSchema *jsonschema.Schema
	compileErr     error
)

func schema() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		// The validator library reports propertyNames violations with unreliable
		// instance locations, so map-key rules are checked in Go instead
		// (see keyErrors); the published schema still documents them.
		var tree any
		if err := json.Unmarshal(schemaJSON, &tree); err != nil {
			compileErr = fmt.Errorf("manifest: embedded schema is not valid JSON: %w", err)
			return
		}
		stripKey(tree, "propertyNames")
		stripped, _ := json.Marshal(tree)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(stripped))
		if err != nil {
			compileErr = fmt.Errorf("manifest: embedded schema is not valid JSON: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(SchemaID, doc); err != nil {
			compileErr = err
			return
		}
		compiledSchema, compileErr = c.Compile(SchemaID)
	})
	return compiledSchema, compileErr
}

var printer = message.NewPrinter(language.English)

// validateSchema checks a decoded JSON document against the schema and
// returns one FieldError per violated leaf constraint.
func validateSchema(doc any) ([]FieldError, error) {
	sch, err := schema()
	if err != nil {
		return nil, err
	}
	err = sch.Validate(doc)
	if err == nil {
		return nil, nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return nil, err
	}
	var out []FieldError
	collectSchemaErrors(ve, &out)
	return out, nil
}

func collectSchemaErrors(ve *jsonschema.ValidationError, out *[]FieldError) {
	collectAt(ve, "", out)
}

// collectAt walks the error tree.
func collectAt(ve *jsonschema.ValidationError, at string, out *[]FieldError) {
	if len(ve.Causes) == 0 {
		path := pointer(ve.InstanceLocation)
		if at != "" {
			path = at
		}
		*out = append(*out, FieldError{Path: path, Message: schemaMessage(ve.ErrorKind)})
		return
	}
	for _, c := range ve.Causes {
		collectAt(c, at, out)
	}
}

// patternHints explains the patterns in schema.json in plain language.
var patternHints = map[string]string{
	"^[a-z][a-z0-9-]{0,39}$":  "must be a slug: a lowercase letter followed by up to 39 lowercase letters, digits or dashes",
	"^[a-z][a-z0-9.-]{0,63}$": "must be a topic name: a lowercase letter followed by up to 63 lowercase letters, digits, dots or dashes, such as \"order.created\"",
	"^[A-Z_][A-Z0-9_]*$":      "must be UPPER_SNAKE_CASE: letters A-Z, digits and underscores, not starting with a digit",
	"^/":                      "must start with \"/\"",
	"^[a-z][a-z0-9_-]*$":      "must be a lowercase extension name such as \"vector\" or \"pg_cron\"",
	cronPattern:               "must be 5 space-separated cron fields (minute hour day-of-month month day-of-week, using digits and * , - / ? or month/day names) such as \"*/15 * * * *\", or one of @hourly, @daily, @weekly, @monthly",
	routePattern:              "must be a hostname with an optional path prefix, such as \"shop\", \"example.com\" or \"example.com/api\"",
}

const cronPattern = `^(@(hourly|daily|weekly|monthly)|[A-Za-z0-9*,/?-]+( [A-Za-z0-9*,/?-]+){4})$`

const routePattern = `^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*(/[A-Za-z0-9._~/-]*)?$`

// schemaMessage turns a library error kind into agent-friendly prose.
func schemaMessage(k jsonschema.ErrorKind) string {
	switch k := k.(type) {
	case *kind.AdditionalProperties:
		return "unknown field(s): " + strings.Join(slices.Sorted(slices.Values(k.Properties)), ", ") + "; check the spelling against the schema"
	case *kind.Required:
		return "missing required field(s): " + strings.Join(k.Missing, ", ")
	case *kind.Pattern:
		if h, ok := patternHints[k.Want]; ok {
			return fmt.Sprintf("%q %s", k.Got, h)
		}
	case *kind.Type:
		return fmt.Sprintf("expected %s, got %s", strings.Join(k.Want, " or "), k.Got)
	case *kind.Minimum:
		return fmt.Sprintf("must be at least %s (got %s)", k.Want.RatString(), k.Got.RatString())
	case *kind.Maximum:
		return fmt.Sprintf("must be at most %s (got %s)", k.Want.RatString(), k.Got.RatString())
	case *kind.MultipleOf:
		got, _ := k.Got.Float64()
		want, _ := k.Want.Float64()
		return fmt.Sprintf("must be a multiple of %v (got %v)", want, got)
	case *kind.MinLength:
		return "must not be empty"
	}
	return k.LocalizedString(printer)
}

func pointer(loc []string) string {
	var b strings.Builder
	for _, p := range loc {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(p))
	}
	return b.String()
}

// decodeAny decodes raw JSON into generic values with numbers preserved, as
// the schema library expects.
func decodeAny(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("unexpected data after top-level JSON value")
	}
	return v, nil
}

func stripKey(v any, key string) {
	switch v := v.(type) {
	case map[string]any:
		delete(v, key)
		for _, c := range v {
			stripKey(c, key)
		}
	case []any:
		for _, c := range v {
			stripKey(c, key)
		}
	}
}
