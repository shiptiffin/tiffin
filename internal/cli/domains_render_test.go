package cli

import (
	"bytes"
	"strings"
	"testing"
)

func ttyApp(out *bytes.Buffer) *app {
	yes := true
	return &app{io: IO{Out: out, Err: out, TTY: &yes, Env: func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}}}
}

func TestRenderDomains(t *testing.T) {
	var out bytes.Buffer
	a := ttyApp(&out)
	list := `[{"domain":"reelbird.app","project":"reelbird","state":"waiting_for_dns","reason":"it points to Namecheap's parking page (162.255.119.104), not this box (198.51.100.7)",
	"records":[{"type":"A","name":"reelbird.app","host":"@","value":"198.51.100.7"}],"found":["162.255.119.104"],
	"registrar":{"name":"Namecheap","where":"Domain List › Manage › Advanced DNS","parked":true,"parking":"Namecheap's parking page","remove":"the URL Redirect Record on @ and the CNAME Record for www (parkingpage.namecheap.com)"},
	"checked":"Checked 2 min ago · next check in 30 s","url":"https://reelbird.app","routes":[],"since":"2026-10-13T12:00:00Z","summary":"x"},
	{"domain":"www.reelbird.app","project":"reelbird","redirectTo":"reelbird.app","state":"live","records":[],"checked":"Checked just now","url":"https://www.reelbird.app",
	"certificate":{"state":"live","issuer":"Let's Encrypt YE2"}}]`
	a.emit(200, []byte(list))
	got := out.String()
	for _, want := range []string{
		"reelbird.app  waiting for DNS",
		"It points to Namecheap's parking page (162.255.119.104), not this box (198.51.100.7).",
		"Parked: In Namecheap › Domain List › Manage › Advanced DNS, delete the URL Redirect Record on @",
		"A  @  198.51.100.7  (reelbird.app)",
		"Checked 2 min ago · next check in 30 s. Check now: tiffin domains check reelbird reelbird.app",
		"www.reelbird.app  live  (redirects to reelbird.app)",
		"https://www.reelbird.app  certificate: Let's Encrypt YE2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// The plan answer of domains add lists the records before anything changes.
	out.Reset()
	plan := `{"status":428,"code":"confirm_required","detail":"confirmation required","plan":{"hash":"0123456789abcdef","summary":"1 to change","ops":[]},
	"records":[{"type":"A","name":"reelbird.app","host":"@","value":"198.51.100.7"},{"type":"CNAME","name":"www.reelbird.app","host":"www","value":"reelbird.app"}]}`
	a.emit(428, []byte(plan))
	got = out.String()
	if !strings.Contains(got, "DNS records to add at your DNS host") || !strings.Contains(got, "CNAME  www  reelbird.app  (www.reelbird.app)") ||
		!strings.Contains(got, "--confirm 0123456789ab") {
		t.Errorf("plan:\n%s", got)
	}

	// domains add, applied: the change, then the domain with its records.
	out.Reset()
	added := `{"applied":true,"change":{"id":"chg_1","project":"reelbird","version":3,"plan":{"summary":"1 to change"}},"plan":{"hash":"x","ops":[]},
	"domain":{"domain":"shop.example.com","project":"reelbird","state":"waiting_for_dns","reason":"no A or AAAA record yet","records":[{"type":"A","name":"shop.example.com","host":"shop","value":"198.51.100.7"}],
	"alternative":[{"type":"CNAME","name":"shop.example.com","host":"shop","value":"box.example"}],"checked":"Checked just now · next check in 30 s","url":"https://shop.example.com"}}`
	a.emit(200, []byte(added))
	got = out.String()
	for _, want := range []string{"✓ applied chg_1", "Add at your DNS host:", "Or instead:", "CNAME  shop  box.example", "tiffin undo chg_1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestDeployNamesOwnDomain: tiffin deploy names the app's live domain and
// lists its other addresses.
func TestDeployNamesOwnDomain(t *testing.T) {
	var out bytes.Buffer
	a := ttyApp(&out)
	a.printDeploys([]*rtDeploy{{ID: "dep_1", Project: "reelbird", App: "site", Status: "live", AppURL: "https://reelbird.app",
		OtherURLs: []string{"https://reelbird.shiptiffin.app"}, URL: "https://d-1--reelbird.shiptiffin.app"}}, nil)
	got := out.String()
	if !strings.Contains(got, "site is live at https://reelbird.app (deploy dep_1") || !strings.Contains(got, "also at: https://reelbird.shiptiffin.app") {
		t.Errorf("deploy output:\n%s", got)
	}
}

// domains remove and projects destroy say what happens to DNS records: in
// the plan, then after the change.
func TestRenderDNSCleanup(t *testing.T) {
	var out bytes.Buffer
	a := ttyApp(&out)
	plan := `{"status":428,"code":"confirm_required","detail":"confirmation required","plan":{"hash":"0123456789abcdef","summary":"1 to change","ops":[]},
	"dns":{"hosts":["shop.example.com"],"delete":[{"type":"A","name":"shop.example.com","host":"shop","value":"198.51.100.7"}],
	"byHand":[{"type":"AAAA","name":"shop.example.com","host":"shop","value":"2001:db8::7"}],"kept":[{"type":"CNAME","name":"www.shop.example.com","host":"www.shop","value":"elsewhere.example"}],"summary":"x"}}`
	a.emit(428, []byte(plan))
	got := out.String()
	for _, want := range []string{
		"deleted once applied (--keep-dns keeps them):", "A  shop  198.51.100.7  (shop.example.com)",
		"Delete these at your DNS host", "AAAA  shop  2001:db8::7",
		"Left alone, as they point elsewhere now:", "--confirm 0123456789ab",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan: missing %q in:\n%s", want, got)
		}
	}

	out.Reset()
	done := `{"applied":true,"change":{"id":"chg_1","project":"shop","version":0,"plan":{"summary":"3 to delete"}},
	"dns":{"hosts":["shop.example.com"],"deleted":[{"type":"A","name":"shop.example.com","host":"shop","value":"198.51.100.7"}],"error":"fake refused","summary":"x"}}`
	a.emit(200, []byte(done))
	got = out.String()
	for _, want := range []string{"✓ applied chg_1", "✓ Deleted the DNS records the box created:", "A  shop  198.51.100.7", "! The DNS provider failed: fake refused", "undo with tiffin undo chg_1"} {
		if !strings.Contains(got, want) {
			t.Errorf("applied: missing %q in:\n%s", want, got)
		}
	}
}
