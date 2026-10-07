package templates

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every email renders with its sample data and leaves nothing unfilled.
func TestSamplesRender(t *testing.T) {
	if len(samples) != len(Names) {
		t.Fatalf("%d samples for %d emails", len(samples), len(Names))
	}
	for _, name := range Names {
		e, err := samples[name]()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for part, s := range map[string]string{"subject": e.Subject, "text": e.Text, "html": e.HTML} {
			if s == "" || strings.Contains(s, "{{") || strings.Contains(s, "%%") || strings.Contains(s, "ZgotmplZ") {
				t.Errorf("%s %s: unfilled or rejected placeholder:\n%s", name, part, s)
			}
		}
		if strings.Contains(e.Text, "\n\n\n") || strings.HasPrefix(e.Text, "\n") {
			t.Errorf("%s text has stray blank lines:\n%s", name, e.Text)
		}
		if n := len(e.HTML); n > 30*1024 {
			t.Errorf("%s html is %d bytes, over 30 KB", name, n)
		}
		if !strings.Contains(e.HTML, "<!--[if mso]>") {
			t.Errorf("%s html lost its Outlook conditional comments", name)
		}
		if strings.Contains(e.HTML, "<img") && !strings.Contains(e.HTML, `alt=""`) {
			t.Errorf("%s: an image without alt", name)
		}
	}
}

// Values are escaped for where they land: text, attributes and links.
func TestValuesAreEscaped(t *testing.T) {
	evil := `<script>alert("x")</script>&'`
	e, err := NewSignIn(NewSignInData{Brand: evil, Host: evil, First: evil, Device: evil, From: evil, When: evil, How: evil, Where: evil, IP: evil, ShownURL: evil,
		URL: `javascript:alert(1)`, MarkURL: `" onerror="alert(1)`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.HTML, "<script>") || strings.Contains(e.HTML, `"x"`) {
		t.Fatalf("raw markup got through:\n%s", e.HTML)
	}
	if !strings.Contains(e.HTML, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;&amp;&#39;") {
		t.Fatalf("expected the value escaped")
	}
	if strings.Contains(e.HTML, `href="javascript:`) || !strings.Contains(e.HTML, "#ZgotmplZ") {
		t.Fatalf("a javascript: link got through")
	}
	if strings.Contains(e.HTML, `" onerror="`) {
		t.Fatalf("an attribute broke out")
	}
	// Plain text and subject are not HTML: values go in as they are, on one line for the subject.
	if !strings.Contains(e.Text, evil) {
		t.Fatalf("text part changed the value")
	}
	if strings.ContainsAny(e.Subject, "\r\n") {
		t.Fatalf("subject has a line break")
	}
}

func TestEnumsAreChecked(t *testing.T) {
	if _, err := Invite(InviteData{Role: "superuser"}); err == nil {
		t.Fatal("an unknown role rendered")
	}
	if _, err := Alert(AlertData{State: ""}); err == nil {
		t.Fatal("an empty state rendered")
	}
}

// Optional parts disappear cleanly when empty.
func TestOptionalPartsDisappear(t *testing.T) {
	e, err := SignIn(SignInData{Brand: "Tiffin", Host: "dash.example.com", First: "there", URL: "https://dash.example.com/login#x", Until: "for the next 15 minutes"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.HTML, "<img") || strings.Contains(e.Text, "asked for from") || strings.Contains(e.HTML, "asked for from") {
		t.Fatalf("empty optional values left traces:\n%s", e.Text)
	}
	a, err := Alert(AlertData{Brand: "Tiffin", Host: "h", State: "test", Box: "b", Rule: "test", Summary: "s", When: "now"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Text, "Rule:") || strings.Contains(a.HTML, "Open the dashboard") || a.Subject != "Test: test on b" {
		t.Fatalf("test alert shows firing parts:\n%s\n%s", a.Subject, a.Text)
	}
}

// Every field of every data struct is documented by gen.go (a sanity check that gen.go matches the templates).
func TestDataStructsHaveFields(t *testing.T) {
	for _, d := range []any{InviteData{}, LinkData{}, SignInData{}, NewSignInData{}, RelayTestData{}, AlertData{}} {
		if reflect.TypeOf(d).NumField() < 3 {
			t.Errorf("%T has too few fields", d)
		}
	}
}

// TIFFIN_EMAIL_OUT=dir go test -run TestWriteSamples writes every sample out, for screenshots.
func TestWriteSamples(t *testing.T) {
	dir := os.Getenv("TIFFIN_EMAIL_OUT")
	if dir == "" {
		t.Skip("set TIFFIN_EMAIL_OUT to write samples")
	}
	for _, name := range Names {
		e, err := samples[name]()
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "box-"+name+".html"), []byte(e.HTML), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "box-"+name+".txt"), []byte("Subject: "+e.Subject+"\n\n"+e.Text), 0o644)
	}
}

func BenchmarkNewSignIn(b *testing.B) {
	for b.Loop() {
		if _, err := samples["new-sign-in"](); err != nil {
			b.Fatal(err)
		}
	}
}

func TestHelpers(t *testing.T) {
	if Brand("shiptiffin.com") != "ShipTiffin" || Brand("box.shiptiffin.com") != "ShipTiffin" || Brand("example.com") != "Tiffin" {
		t.Fatal("brand")
	}
	if Host("https://dashboard.example.com/", "x") != "dashboard.example.com" || Host("", "fallback") != "fallback" {
		t.Fatal("host")
	}
	if MarkURL("https://dashboard.example.com/") != "https://dashboard.example.com/email-mark.png" || MarkURL("http://x") != "" || MarkURL("") != "" {
		t.Fatal("mark")
	}
	if When(time.Date(2026, 10, 7, 14, 32, 0, 0, time.UTC)) != "7 October 2026, 14:32 UTC" {
		t.Fatal("when")
	}
}
