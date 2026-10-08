package portable

import (
	"io"
	"strings"
	"testing"
)

// An archive's database.sql reaches psql in restricted mode, entered with a
// key the archive cannot know; its own restrict lines are dropped and any
// other backslash command is left for psql to refuse.
func TestRestrictedScript(t *testing.T) {
	long := strings.Repeat("x", 200_000)
	in := "\\restrict AbC123\nSELECT 1;\n\\! touch /var/lib/tiffin/pwned\nCOPY t FROM stdin;\n" + long + "\n\\.\n\\unrestrict AbC123\nSELECT 2;"
	out, err := io.ReadAll(restricted(strings.NewReader(in), "Key42"))
	if err != nil {
		t.Fatal(err)
	}
	want := "\\restrict Key42\nSELECT 1;\n\\! touch /var/lib/tiffin/pwned\nCOPY t FROM stdin;\n" + long + "\n\\.\nSELECT 2;"
	if string(out) != want {
		t.Fatalf("got %d bytes, starting %q", len(out), string(out[:min(len(out), 80)]))
	}
	if !strings.HasPrefix(string(out), "\\restrict Key42\n") || strings.Contains(string(out), "AbC123") {
		t.Fatal("the archive's own restrict lines must go and ours come first")
	}
}

// COPY data lines that look like restrict lines or other backslash commands
// are data: every one reaches psql. Only the dump's own header (among the
// banner comments) and the lines with its key are dropped.
func TestRestrictedKeepsCopyData(t *testing.T) {
	key := "Zx9" + strings.Repeat("a1", 30)
	data := []string{
		"1\t\\restrict abc123", // a value CR + "estrict abc123"
		"\\restrict abc123",
		"\\unrestrict abc123",
		"\\restrict " + key[:10],
		"\\unrestrict",
		"\\! rm -rf /",
		"\\\\.", // an escaped backslash then a dot: data, not the end marker
		"\\N",
		"--",
	}
	in := "--\n-- PostgreSQL database dump\n--\n\n\\restrict " + key + "\n\n" +
		"-- Dumped from database version 18.0\n\nSET statement_timeout = 0;\n" +
		"COPY \"public\".\"t\" (\"v\") FROM stdin;\n" + strings.Join(data, "\n") + "\n\\.\n\n" +
		"CREATE FUNCTION f() RETURNS text LANGUAGE sql AS $$\nSELECT '\n\\restrict abc123\n'\n$$;\n" +
		"--\n-- PostgreSQL database dump complete\n--\n\n\\unrestrict " + key + "\n\n"
	out, err := io.ReadAll(restricted(strings.NewReader(in), "Key42"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	want := "\\restrict Key42\n" + strings.Replace(strings.Replace(in, "\\restrict "+key+"\n", "", 1), "\\unrestrict "+key+"\n", "", 1)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, key) {
		t.Fatal("the dump's own key must not reach psql")
	}
}

// Without the header among the banner comments nothing is dropped: a later
// restrict line, whatever its key, is left for psql (which refuses it outside
// COPY data, so a forged script fails loudly rather than losing rows).
func TestRestrictedHeaderOnlyInPreamble(t *testing.T) {
	in := "SET x = 1;\n\\restrict abc\n\\unrestrict abc\n"
	out, err := io.ReadAll(restricted(strings.NewReader(in), "Key42"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "\\restrict Key42\n"+in {
		t.Fatalf("got %q", out)
	}
	// A second header-looking line after the first is passed on too.
	in = "\\restrict abc\n\\restrict def\nSELECT 1;\n\\unrestrict def\n\\unrestrict abc\n"
	out, err = io.ReadAll(restricted(strings.NewReader(in), "Key42"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "\\restrict Key42\n\\restrict def\nSELECT 1;\n\\unrestrict def\n" {
		t.Fatalf("got %q", out)
	}
}
