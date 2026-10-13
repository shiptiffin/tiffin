package manifest

import (
	"errors"
	"strings"
	"testing"
)

// TestPrimaryDomainValidation: at most one primary per app, served whole
// by that app, with field errors that say what to change.
func TestPrimaryDomainValidation(t *testing.T) {
	cases := []struct {
		name    string
		apps    map[string][]string // app → routes
		domains map[string]Domain
		want    []string // "<path>: <message part>"; none means valid
	}{
		{
			name:    "one primary",
			apps:    map[string][]string{"web": {"web", "example.com", "old.com"}},
			domains: map[string]Domain{"example.com": {WWW: WWWRedirect, Primary: true}, "old.com": {}},
		},
		{
			name:    "a primary per app",
			apps:    map[string][]string{"web": {"web", "example.com"}, "docs": {"docs", "docs.example.com"}},
			domains: map[string]Domain{"example.com": {Primary: true}, "docs.example.com": {Primary: true}},
		},
		{
			name:    "a path of the domain on another app is fine",
			apps:    map[string][]string{"web": {"web", "example.com"}, "api": {"example.com/api"}},
			domains: map[string]Domain{"example.com": {Primary: true}},
		},
		{
			name:    "two primaries on one app",
			apps:    map[string][]string{"web": {"web", "a.com", "b.com"}},
			domains: map[string]Domain{"a.com": {Primary: true}, "b.com": {Primary: true}},
			want:    []string{`/domains/b.com/primary: app "web" already has the primary domain "a.com"`},
		},
		{
			name:    "only a path is served",
			apps:    map[string][]string{"web": {"web"}, "api": {"example.com/api"}},
			domains: map[string]Domain{"example.com": {Primary: true}},
			want:    []string{`/domains/example.com/primary: a primary domain is served whole by one app, but only paths of "example.com" are routes`},
		},
		{
			name:    "no app serves it",
			apps:    map[string][]string{"web": {"web"}},
			domains: map[string]Domain{"example.com": {Primary: true}},
			want:    []string{`/domains/example.com: no app serves "example.com"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manifest{Version: 1, Project: "shop", Apps: map[string]App{}, Domains: tc.domains}
			for name, routes := range tc.apps {
				m.Apps[name] = App{Routes: routes}
			}
			Normalize(m)
			err := Validate(m)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want a validation error, got %v", err)
			}
			if len(ve.Errors) != len(tc.want) {
				t.Fatalf("errors = %v, want %d", ve.Errors, len(tc.want))
			}
			for i, w := range tc.want {
				if got := ve.Errors[i].Path + ": " + ve.Errors[i].Message; !strings.HasPrefix(got, w) {
					t.Errorf("error %d = %q, want it to start with %q", i, got, w)
				}
			}
		})
	}
}
