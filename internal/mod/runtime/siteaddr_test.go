package runtime

import (
	"slices"
	"testing"

	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// TestSiteURLs: after a deploy, an app with a live domain of its own is
// named by that domain, with its other addresses beside it.
func TestSiteURLs(t *testing.T) {
	live := map[string]bool{}
	old := liveDomain
	liveDomain = func(h string) bool { return live[h] }
	t.Cleanup(func() { liveDomain = old })
	r := &rt{p: &platform.Platform{Domain: "shiptiffin.app", PublicURL: "https://dashboard.shiptiffin.app"}}
	for _, tc := range []struct {
		name   string
		routes []string
		live   []string
		main   string
		others []string
	}{
		{"no domain", []string{"reelbird"}, nil, "https://reelbird.shiptiffin.app", nil},
		{"domain waiting for DNS", []string{"reelbird", "reelbird.app"}, nil, "https://reelbird.shiptiffin.app", nil},
		{"domain live", []string{"reelbird", "reelbird.app"}, []string{"reelbird.app"}, "https://reelbird.app", []string{"https://reelbird.shiptiffin.app"}},
		{"first live of two", []string{"reelbird", "reelbird.app", "reelbird.com"}, []string{"reelbird.com"}, "https://reelbird.com", []string{"https://reelbird.shiptiffin.app"}},
		{"both live", []string{"reelbird", "reelbird.app", "reelbird.com"}, []string{"reelbird.app", "reelbird.com"}, "https://reelbird.app",
			[]string{"https://reelbird.shiptiffin.app", "https://reelbird.com"}},
		{"a path on its domain", []string{"shop-api", "example.com/api"}, []string{"example.com"}, "https://example.com/api", []string{"https://shop-api.shiptiffin.app"}},
		{"a name on the box is not its own domain", []string{"reelbird", "blog.shiptiffin.app"}, nil, "https://reelbird.shiptiffin.app", []string{"https://blog.shiptiffin.app"}},
	} {
		clear(live)
		for _, h := range tc.live {
			live[h] = true
		}
		main, others := r.siteURLs("reelbird", "site", &manifest.App{Routes: tc.routes})
		if main != tc.main || !slices.Equal(others, tc.others) {
			t.Errorf("%s: %q %v, want %q %v", tc.name, main, others, tc.main, tc.others)
		}
	}
	// A production deploy's record carries them; a preview keeps its own.
	live["reelbird.app"] = true
	spec := &manifest.App{Routes: []string{"reelbird", "reelbird.app"}}
	d := &Deploy{ID: "dep_01JA2B3C4D5E6F7G8H9J0KMNPQ", Project: "reelbird", App: "site", Status: StatusLive}
	r.present(d, spec)
	if d.AppURL != "https://reelbird.app" || !slices.Equal(d.OtherURLs, []string{"https://reelbird.shiptiffin.app"}) {
		t.Errorf("deploy: %q %v", d.AppURL, d.OtherURLs)
	}
	p := &Deploy{ID: "dep_01JA2B3C4D5E6F7G8H9J0KMNPR", Project: "reelbird", App: "site", Preview: "pr-1", Status: StatusLive}
	r.present(p, spec)
	if p.AppURL != "https://pr-1--reelbird.shiptiffin.app" || p.OtherURLs != nil {
		t.Errorf("preview: %q %v", p.AppURL, p.OtherURLs)
	}
}
