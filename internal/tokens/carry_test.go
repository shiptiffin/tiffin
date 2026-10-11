package tokens

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/shiptiffin/tiffin/internal/state"
)

// Restoring a backup from before the owner first signed in (or importing
// another box's state) must not reopen the hand-off, nor bring back a link,
// session or key this box has spent, ended or revoked since.
func TestCarryIntoKeepsSignInsClosed(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "ann@example.com", RoleAdmin)

	handoff, _, err := m.BootstrapLink(ctx, owner, BootstrapTTL) // the setup link, not yet used
	if err != nil {
		t.Fatal(err)
	}
	unused, _, _ := m.CreateLoginLink(ctx, owner) // a `tiffin login` link nobody has opened
	ended, _ := session(t, m, ann.ID, MethodPasskey)
	kept, _ := session(t, m, ann.ID, MethodPasskey)
	key, k, err := m.CreateKey(ctx, owner, allFull)
	if err != nil {
		t.Fatal(err)
	}

	// The backup is taken here.
	backup := filepath.Join(t.TempDir(), "backup.db")
	if _, err := m.db.SQL().ExecContext(ctx, `VACUUM INTO ?`, backup); err != nil {
		t.Fatal(err)
	}

	// Since then: the owner signed in with the hand-off link, Ann signed one
	// session out, and the key was revoked.
	sess, _, _, err := m.RedeemLoginLink(ctx, handoff)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := m.Authenticate(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	if done, _ := m.HandoffDone(ctx); !done {
		t.Fatal("the hand-off is not closed")
	}
	annP, _ := m.Authenticate(ctx, kept)
	endedP, _ := m.Authenticate(ctx, ended)
	if _, err := m.EndSession(ctx, annP, endedP.TokenID); err != nil {
		t.Fatal(err)
	}
	if err := m.Revoke(ctx, owner, k.ID); err != nil {
		t.Fatal(err)
	}

	// The backup's state, about to replace this box's.
	in, err := state.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	tx, err := in.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CarryInto(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got := NewManager(in)
	if done, err := got.HandoffDone(ctx); err != nil || !done {
		t.Fatalf("the hand-off reopened: %v %v", done, err)
	}
	if _, _, err := got.HandoffLink(ctx, BootstrapTTL); !errors.Is(err, ErrHandoffDone) {
		t.Fatalf("a new hand-off link after the restore: %v", err)
	}
	if _, _, _, err := got.RedeemLoginLink(ctx, handoff); err == nil {
		t.Fatal("the spent hand-off link works again")
	}
	if _, err := got.Authenticate(ctx, ended); err == nil {
		t.Fatal("a session signed out since the backup works again")
	}
	if _, err := got.Authenticate(ctx, key); err == nil {
		t.Fatal("a key revoked since the backup works again")
	}
	// What still works here keeps working.
	if _, err := got.Authenticate(ctx, kept); err != nil {
		t.Fatalf("an open session: %v", err)
	}
	if _, _, _, err := got.RedeemLoginLink(ctx, unused); err != nil {
		t.Fatalf("an unused, unexpired link: %v", err)
	}

	// Another box's state (an import): none of its links or sessions are
	// this box's, so none work; its hand-off closes with this box's.
	other, oOwner, _ := setup(t)
	oLink, _, _ := other.BootstrapLink(ctx, oOwner, BootstrapTTL)
	oSess, _ := session(t, other, OwnerPerson, MethodPasskey)
	tx, _ = other.db.SQL().BeginTx(ctx, nil)
	if err := m.CarryInto(ctx, tx); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	if done, _ := other.HandoffDone(ctx); !done {
		t.Fatal("imported state reopened the hand-off")
	}
	if _, _, _, err := other.RedeemLoginLink(ctx, oLink); err == nil {
		t.Fatal("an imported hand-off link works")
	}
	if _, err := other.Authenticate(ctx, oSess); err == nil {
		t.Fatal("an imported session works")
	}
}
