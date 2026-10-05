package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/boxfile"
)

func writeTestArchive(t *testing.T, m *boxfile.Manifest) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "box.tiffin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, err := boxfile.NewWriter(f, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// box import checks the archive and the key on this computer, before
// anything is uploaded (exit 3 with a precise message).
func TestBoxImportChecksLocally(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	other, _ := age.GenerateX25519Identity()
	archive := writeTestArchive(t, &boxfile.Manifest{Kind: boxfile.ManifestKind, Format: boxfile.FormatVersion, Projects: []string{"shop"},
		Recipient: id.Recipient().String()})
	env := newEnv(t)
	keyFile := func(k *age.X25519Identity) string {
		p := filepath.Join(t.TempDir(), "box.key")
		_ = os.WriteFile(p, []byte(k.String()+"\n"), 0o600)
		return p
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"not an archive", []string{"box", "import", keyFile(id)}, "not a Tiffin export"},
		{"missing file", []string{"box", "import", filepath.Join(t.TempDir(), "nope.tiffin")}, "no such file"},
		{"no key", []string{"box", "import", archive}, "--key-file"},
		{"wrong key", []string{"box", "import", archive, "--key-file", keyFile(other)}, "is not the one"},
	}
	for _, c := range cases {
		code, out, _ := run(t, env, c.args...)
		if code != ExitInvalid || !strings.Contains(string(out), c.want) {
			t.Errorf("%s: exit %d, want 3 with %q: %s", c.name, code, c.want, out)
		}
	}
	// The right key passes the local checks; a local --home directory is no box to import into.
	code, out, _ := run(t, env, "box", "import", archive, "--key-file", keyFile(id))
	if code != ExitInvalid || !strings.Contains(string(out), "works with a box") {
		t.Errorf("local home: exit %d %s", code, out)
	}
	newer := writeTestArchive(t, &boxfile.Manifest{Kind: boxfile.ManifestKind, Format: boxfile.FormatVersion + 1, IncludesKey: true})
	if code, out, _ := run(t, env, "box", "import", newer); code != ExitInvalid || !strings.Contains(string(out), "newer Tiffin") {
		t.Errorf("newer archive: exit %d %s", code, out)
	}
	if code, out, _ := run(t, env, "box", "export"); code != ExitInvalid || !strings.Contains(string(out), "works with a box") {
		t.Errorf("export from a local home: exit %d %s", code, out)
	}
}
