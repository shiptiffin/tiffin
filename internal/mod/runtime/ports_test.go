package runtime

import (
	"context"
	"testing"
)

// A delayed cleanup (a drain that ends after the project was deleted and
// its port given to another app) frees nothing it no longer holds.
func TestDelayedCleanupKeepsAReassignedPort(t *testing.T) {
	h := newHarness(t)
	p, err := h.r.allocPort("tf.gone.web.prod.1")
	if err != nil {
		t.Fatal(err)
	}
	h.r.freeName("tf.gone.web.prod.1") // the deletion sweep removed it
	q, err := h.r.allocPort("tf.other.web.prod.1")
	if err != nil || q != p {
		t.Fatalf("port %d reallocated as %d (%v)", p, q, err)
	}
	h.r.removeInstances(context.Background(), []Instance{{Name: "tf.gone.web.prod.1", Port: p}}) // the drain's cleanup
	if got := h.r.ownedNames()["tf.other.web.prod.1"]; got != p {
		t.Fatalf("the new container's port reservation was erased: %d", got)
	}
}

// A container whose removal failed keeps its port reserved (it may still
// listen) until the leftover sweep removes it.
func TestFailedRemovalKeepsThePortUntilTheSweep(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := "tf.shop.api.prod.99"
	p, err := h.r.allocPort(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Run(ctx, RunSpec{Name: name, Image: "x", Port: p}); err != nil {
		t.Fatal(err)
	}
	_ = h.eng.Stop(ctx, name, 0)
	h.eng.setStuck(true)
	h.r.removeInstances(ctx, []Instance{{Name: name, Port: p}})
	h.r.mu.Lock()
	held := h.r.ports[p]
	h.r.mu.Unlock()
	if held != name {
		t.Fatalf("port %d freed while its container could not be removed (%q)", p, held)
	}
	h.eng.setStuck(false)
	h.r.removeOrphans(ctx)
	h.r.mu.Lock()
	held = h.r.ports[p]
	h.r.mu.Unlock()
	if held != "" {
		t.Fatalf("port %d still reserved after the sweep removed its container (%q)", p, held)
	}
}
