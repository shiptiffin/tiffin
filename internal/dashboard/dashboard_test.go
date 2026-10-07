package dashboard

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Connect GitHub POSTs a form to GitHub: the dashboard's CSP must allow
// exactly that origin as a form target (and nothing it isn't given).
func TestCSPAllowsTheGitHubForm(t *testing.T) {
	get := func() string {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest("GET", "/settings/git", nil))
		return w.Header().Get("Content-Security-Policy")
	}
	if c := get(); !strings.HasSuffix(c, "form-action 'self' https://github.com") || !strings.Contains(c, "default-src 'self'") {
		t.Fatalf("CSP: %s", c)
	}
	old := FormOrigins.Load()
	t.Cleanup(func() { FormOrigins.Store(old) })
	ghe := func() []string {
		return []string{"https://ghe.example.com", "https://x.com; script-src *", "http://plain.example"}
	}
	FormOrigins.Store(&ghe)
	if c := get(); !strings.HasSuffix(c, "form-action 'self' https://ghe.example.com") {
		t.Fatalf("CSP with GitHub Enterprise: %s", c)
	}
}
