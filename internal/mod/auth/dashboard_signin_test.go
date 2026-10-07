package auth

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestDashboardKeys(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	d := dashboardKeys{p}
	if got := d.Configured(ctx); len(got) != 0 {
		t.Fatalf("no keys yet: %v", got)
	}
	google, _ := ProviderByID("google")
	discord, _ := ProviderByID("discord")
	for _, c := range []struct {
		prov Provider
		in   ProviderInput
	}{
		{google, ProviderInput{BoxProvider: BoxProvider{ClientID: "box-google"}, ClientSecret: "box-google-secret"}},
		{discord, ProviderInput{BoxProvider: BoxProvider{ClientID: "box-discord"}, ClientSecret: "box-discord-secret"}},
	} {
		if err := setProvider(ctx, p, c.prov, c.in, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	if got := d.Configured(ctx); !slices.Equal(got, []string{"google"}) {
		t.Fatalf("configured: %v (only Google and GitHub sign in to the dashboard)", got)
	}
	c, ok, err := d.Client(ctx, "google")
	if err != nil || !ok || c.ClientID != "box-google" || c.ClientSecret != "box-google-secret" || c.RedirectURL != CallbackURL(p, "google") {
		t.Fatalf("google client: %+v %v %v", c, ok, err)
	}
	for _, id := range []string{"github", "discord", "nope"} {
		if _, ok, err := d.Client(ctx, id); ok || err != nil {
			t.Fatalf("%s: ok=%v err=%v", id, ok, err)
		}
	}
	if _, err := removeProvider(ctx, p, google); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Client(ctx, "google"); ok {
		t.Fatal("removed keys still sign in")
	}
}

// The dashboard's own sign-ins are answered on the dashboard; the apps' go to the engine.
func TestDashboardHandlerSharesTheCallback(t *testing.T) {
	forwarded := 0
	eng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
	defer eng.Close()
	old := engineUpstream
	engineUpstream = eng.URL
	defer func() { engineUpstream = old }()
	DashboardSignIn = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Query().Get("state"), "tdash.") {
			return false
		}
		w.WriteHeader(http.StatusTeapot)
		return true
	}
	defer func() { DashboardSignIn = nil }()
	h := DashboardHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://dashboard.tiffin.localhost/api/auth/callback/github?code=c&state=tdash.abc", nil))
	if rec.Code != http.StatusTeapot || forwarded != 0 {
		t.Fatalf("dashboard sign-in: %d, forwarded %d", rec.Code, forwarded)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://dashboard.tiffin.localhost/api/auth/callback/github?code=c&state=abc", nil))
	if forwarded != 1 {
		t.Fatalf("an app's sign-in was not forwarded: %d", rec.Code)
	}
}
