package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
)

// A managed box's owner starts with the name its setup brought; a box with
// no managed config (`tiffin up`) keeps "Owner" for the dashboard to ask.
func TestNameOwnerFromManagedConfig(t *testing.T) {
	ctx := context.Background()
	old := platform.ManagedConfigPath
	t.Cleanup(func() { platform.ManagedConfigPath = old })

	owner := func(home string) string {
		b, _, err := openBox(ctx, home)
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		var errw bytes.Buffer
		nameOwner(ctx, b.tokens, &errw)
		if errw.Len() > 0 {
			t.Fatal(errw.String())
		}
		p, err := b.tokens.GetPerson(ctx, tokens.OwnerPerson)
		if err != nil {
			t.Fatal(err)
		}
		return p.Name
	}

	platform.ManagedConfigPath = filepath.Join(t.TempDir(), "absent.json")
	if got := owner(t.TempDir()); got != "Owner" {
		t.Fatalf("self-hosted owner is %q", got)
	}

	platform.ManagedConfigPath = filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(platform.ManagedConfigPath, []byte(`{"controlPlane":"https://shiptiffin.com","boxID":"box_1","licence":"tl1.x","ownerName":" Bilal\n"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := owner(t.TempDir()); got != "Bilal" {
		t.Fatalf("managed owner is %q", got)
	}
}
