package manifest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// FieldError is one thing wrong with a manifest.
type FieldError struct {
	// Path is a JSON pointer to the offending value, e.g. "/apps/web/instances".
	// The empty string is the document root.
	Path string `json:"path"`
	// Message says what is wrong and, where possible, how to fix it.
	Message string `json:"message"`
}

// ValidationError lists every problem found in a manifest. Errors are sorted
// by path so output is deterministic.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

// Error joins all field errors, one per line, as "<pointer>: <message>".
func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		p := fe.Path
		if p == "" {
			p = "/"
		}
		lines[i] = p + ": " + fe.Message
	}
	return strings.Join(lines, "\n")
}

func newValidationError(errs []FieldError) *ValidationError {
	slices.SortStableFunc(errs, func(a, b FieldError) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Message, b.Message)
	})
	errs = slices.CompactFunc(errs, func(a, b FieldError) bool { return a == b })
	return &ValidationError{Errors: errs}
}

// Validate checks a manifest against the JSON Schema and the semantic rules
// the schema cannot express (workers have no routes, routes are unique
// across apps, the email sender is an address, crons target real apps). It expects a normalized manifest: zero values of defaulted
// fields (instances 0, memoryMB 0) are reported as errors. It returns nil or
// a *ValidationError.
func Validate(m *Manifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("manifest: marshal: %w", err)
	}
	doc, err := decodeAny(raw)
	if err != nil {
		return fmt.Errorf("manifest: re-decode: %w", err)
	}
	errs, err := validateSchema(doc)
	if err != nil {
		return err
	}
	errs = append(errs, keyErrors(doc)...)
	errs = append(errs, semanticErrors(m)...)
	if len(errs) == 0 {
		return nil
	}
	return newValidationError(errs)
}

// semanticErrors implements the rules JSON Schema cannot express.
func semanticErrors(m *Manifest) []FieldError {
	var errs []FieldError
	owners := map[string]string{} // normalized route -> owning app
	for _, name := range sortedKeys(m.Apps) {
		app := m.Apps[name]
		base := "/apps/" + escapePointer(name)
		if app.Role == RoleWorker && len(app.Routes) > 0 {
			errs = append(errs, FieldError{
				Path:    base + "/routes",
				Message: fmt.Sprintf("worker apps cannot have routes (got %s); remove \"routes\" or set role to \"web\"", quoteList(app.Routes)),
			})
			continue
		}
		for i, r := range app.Routes {
			key := normalizeRoute(r)
			if prev, dup := owners[key]; dup {
				who := fmt.Sprintf("app %q", prev)
				if prev == name {
					who = "this app"
				}
				errs = append(errs, FieldError{
					Path:    fmt.Sprintf("%s/routes/%d", base, i),
					Message: fmt.Sprintf("route %q is already used by %s; routes must be unique across apps", r, who),
				})
				continue
			}
			owners[key] = name
		}
	}
	if e := m.Services.Email; e != nil && e.From != "" {
		if !emailRe.MatchString(e.From) {
			errs = append(errs, FieldError{
				Path:    "/services/email/from",
				Message: fmt.Sprintf("%q is not a valid email address; use a bare address such as \"hello@example.com\", or remove \"from\" to use \"<project>@<box domain>\"", e.From),
			})
		}
	}
	for _, name := range sortedKeys(m.Crons) {
		c := m.Crons[name]
		if _, ok := m.Apps[c.App]; !ok && c.App != "" {
			msg := fmt.Sprintf("cron %q targets app %q, which is not defined in \"apps\"", name, c.App)
			if len(m.Apps) > 0 {
				msg += "; defined apps: " + quoteList(sortedKeys(m.Apps))
			} else {
				msg += "; add the app to \"apps\" first"
			}
			errs = append(errs, FieldError{Path: "/crons/" + escapePointer(name) + "/app", Message: msg})
		}
	}
	return errs
}

var (
	slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	// emailRe accepts a bare address: a local part, "@", and a dotted hostname.
	emailRe  = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
	envKeyRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// keyErrors checks the names used as map keys in a decoded JSON document: app
// and bucket names are slugs, env variable names are UPPER_SNAKE_CASE. It is
// lenient about shape; shape errors are the schema's job.
func keyErrors(doc any) []FieldError {
	var errs []FieldError
	slug := func(base string, obj any) {
		for key := range asMap(obj) {
			if !slugRe.MatchString(key) {
				errs = append(errs, FieldError{Path: base + "/" + escapePointer(key), Message: fmt.Sprintf("name %q %s", key, patternHints[slugRe.String()])})
			}
		}
	}
	env := func(base string, obj any) {
		for key := range asMap(obj) {
			if !envKeyRe.MatchString(key) {
				errs = append(errs, FieldError{Path: base + "/" + escapePointer(key), Message: fmt.Sprintf("variable name %q %s", key, patternHints[envKeyRe.String()])})
			}
		}
	}
	root := asMap(doc)
	env("/env", root["env"])
	apps := asMap(root["apps"])
	slug("/apps", apps)
	for name, app := range apps {
		env("/apps/"+escapePointer(name)+"/env", asMap(app)["env"])
	}
	slug("/crons", root["crons"])
	slug("/services/storage/buckets", asMap(asMap(asMap(root["services"])["storage"])["buckets"]))
	return errs
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
