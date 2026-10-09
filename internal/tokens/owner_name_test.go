package tokens

import (
	"context"
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"  Bilal  Tahir ":       "Bilal Tahir",
		"Ann\nSmith":            "Ann Smith",
		"Ann\x00\x07Smith\t":    "Ann Smith",
		"Ann‮Smith":             "AnnSmith", // a direction override is formatting, not a letter
		"Zoë Ångström":          "Zoë Ångström",
		"":                      "",
		" \n\t ":                "",
		strings.Repeat("a", 64): strings.Repeat("a", 64),
		strings.Repeat("a", 65): "",
		"bad \xff bytes":        "",
	} {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A managed box's owner starts with their account's name, once; a name the
// owner chose is never overwritten.
func TestNameOwner(t *testing.T) {
	ctx := context.Background()

	m, _, _ := setup(t)
	if ok, err := m.NameOwner(ctx, " \n "); err != nil || ok {
		t.Fatalf("an empty name: %v %v", ok, err)
	}
	if ok, err := m.NameOwner(ctx, "  Bilal\tTahir "); err != nil || !ok {
		t.Fatalf("first name: %v %v", ok, err)
	}
	if p, _ := m.GetPerson(ctx, OwnerPerson); p.Name != "Bilal Tahir" {
		t.Fatalf("owner is %q", p.Name)
	}
	// Once only: a later setup name, or the same call again, changes nothing.
	if ok, err := m.NameOwner(ctx, "Someone Else"); err != nil || ok {
		t.Fatalf("second go: %v %v", ok, err)
	}
	if p, _ := m.GetPerson(ctx, OwnerPerson); p.Name != "Bilal Tahir" {
		t.Fatalf("owner is %q", p.Name)
	}

	// The owner renamed themselves first: kept.
	m2, owner2, _ := setup(t)
	if _, err := m2.UpdatePerson(ctx, owner2, OwnerPerson, "Owner", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.UpdatePerson(ctx, owner2, OwnerPerson, "Bee", ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := m2.NameOwner(ctx, "Bilal"); err != nil || ok {
		t.Fatalf("overwrote a chosen name: %v %v", ok, err)
	}
	if p, _ := m2.GetPerson(ctx, OwnerPerson); p.Name != "Bee" {
		t.Fatalf("owner is %q", p.Name)
	}
	// ...and it has had its go: a rename back to "Owner" later stays too.
	if _, err := m2.UpdatePerson(ctx, owner2, OwnerPerson, "Owner", ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m2.NameOwner(ctx, "Bilal"); ok {
		t.Fatal("named the owner a second time")
	}
}
