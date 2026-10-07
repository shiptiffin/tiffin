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
