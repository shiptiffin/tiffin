package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

func TestAnyRoutable(t *testing.T) {
	for ip, want := range map[string]bool{
		"203.0.113.7": true, "2001:db8::1": true,
		"192.168.64.2": false, "10.0.0.5": false, "127.0.0.1": false, "100.72.1.2": false, "fd00::1": false,
	} {
		if got := anyRoutable([]netip.Addr{netip.MustParseAddr(ip)}); got != want {
			t.Errorf("%s: %v, want %v", ip, got, want)
		}
	}
}

func TestBoxReachAppsDomain(t *testing.T) {
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f := reachFlags{domain: "tiffin.localhost", ips: []string{"203.0.113.7"}, tls: "acme"}
	r, domain, url, err := boxReach(t.Context(), db, f)
	if err != nil || domain != "203-0-113-7.sslip.io" || r.AppsDomain != "" || url == "" {
		t.Fatalf("automatic: %v %s %+v", err, domain, r)
	}
	if err := platform.SaveBoxDomain(t.Context(), db, platform.BoxDomain{Domain: "example.com", Apps: "example.app"}); err != nil {
		t.Fatal(err)
	}
	r, domain, url, err = boxReach(t.Context(), db, f)
	if err != nil || domain != "example.com" || r.AppsDomain != "example.app" || url != "https://dashboard.example.com" {
		t.Fatalf("apps domain: %v %s %s %+v", err, domain, url, r)
	}
}

func TestDashboardURL(t *testing.T) {
	for _, c := range []struct{ configured, want string }{
		{"", "https://dashboard.tiffin.localhost:8443"},
		{"https://dashboard.tiffin.localhost:18443", "https://dashboard.tiffin.localhost:18443"},
		{"https://other.example:443/", "https://dashboard.tiffin.localhost"},
		{"http://localhost:7392/", "http://localhost:7392"},
		{"http://127.0.0.1:7392", "http://127.0.0.1:7392"},
	} {
		if got := dashboardURL(c.configured, "dashboard.tiffin.localhost", 8443); got != c.want {
			t.Errorf("%q: %s, want %s", c.configured, got, c.want)
		}
	}
}

// TestDomainSetAppsDomainFlag: --apps-domain reaches the API (the generated
// check command's --apps-domain comes from the API's query parameter).
func TestDomainSetAppsDomainFlag(t *testing.T) {
	var setBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/domain" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &setBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"domain":"example.com","appsDomain":"example.app","state":"issuing","summary":"Switching"}`))
	}))
	defer srv.Close()
	env := map[string]string{"TIFFIN_URL": srv.URL, "TIFFIN_TOKEN": "tfn_x", "TIFFIN_HOME": filepath.Join(t.TempDir(), "unused")}
	if code, out, errs := run(t, env, "domain", "set", "example.com", "--apps-domain", "example.app", "--create-records", "--no-wait"); code != ExitOK {
		t.Fatalf("set: %d %s %s", code, out, errs)
	}
	if setBody["domain"] != "example.com" || setBody["appsDomain"] != "example.app" || setBody["createRecords"] != true {
		t.Errorf("set body: %v", setBody)
	}
	// Without the flag, the body has no appsDomain (one domain, as before).
	setBody = nil
	if code, _, _ := run(t, env, "domain", "set", "example.com", "--no-wait"); code != ExitOK || setBody["appsDomain"] != nil {
		t.Errorf("set without the flag: %d %v", code, setBody)
	}
}
