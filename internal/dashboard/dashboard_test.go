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

// The licences are served as text for the /licenses page (AGPL-3.0 §13).
func TestLicensesText(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("GET", "/licenses.txt", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(body, "AGPL-3.0-only") || !strings.Contains(body, "https://github.com/shiptiffin/tiffin") ||
		!strings.Contains(body, "THIRD-PARTY SOFTWARE IN TIFFIN") {
		t.Fatalf("GET /licenses.txt: %d %q %.200q", w.Code, w.Header().Get("Content-Type"), body)
	}
}
