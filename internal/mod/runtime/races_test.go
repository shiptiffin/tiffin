package runtime

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
)

// startDeploy queues a deploy of app and starts its pipeline without
// waiting for it.
func (h *harness) startDeploy(app, preview string, files map[string]string) *Deploy {
	h.t.Helper()
	d := h.queueDeploy(app, preview)
	h.r.start(d, h.sourceFor(d, files), SourceUpload)
	return d
}

// queueDeploy records a queued deploy of app, as an upload does.
func (h *harness) queueDeploy(app, preview string) *Deploy {
	h.t.Helper()
	ctx := context.Background()
	spec, perr := h.r.checkDeployable(ctx, "shop", app, preview, false)
	if perr != nil {
		h.t.Fatal(perr)
	}
	d, err := h.r.newDeploy(ctx, "shop", app, preview, SourceUpload, "tok_test", spec)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

// sourceFor puts files where a queued deploy's source goes.
func (h *harness) sourceFor(d *Deploy, files map[string]string) string {
	h.t.Helper()
	src := filepath.Join(h.r.workDir(d), "source.tgz")
	if err := os.Rename(h.source(files), src); err != nil {
		h.t.Fatal(err)
	}
	return src
}

// gateOn makes builds and starts that ask for it wait until the returned
// func is called.
func (h *harness) gateOn() (gated chan string, open func()) {
	h.eng.mu.Lock()
	h.eng.gate, h.eng.gated = make(chan struct{}), make(chan string, 16)
	gate, gated := h.eng.gate, h.eng.gated
	h.eng.mu.Unlock()
	return gated, func() { close(gate) }
}

func (h *harness) runningOfApp(app string) int {
	n := 0
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.app"] == app {
			n++
		}
	}
	return n
}

// An older deploy that finishes after a newer one went live never replaces
// it: it is skipped, and its image goes.
func TestOvertakenDeployIsSkipped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old := h.queueDeploy("api", "")
	src := h.sourceFor(old, map[string]string{"index.ts": "old"})
	time.Sleep(2 * time.Millisecond) // IDs order by time
	newer := h.deploy("api", "", map[string]string{"index.ts": "new"})
	if newer.Status != StatusLive {
		t.Fatalf("newer: %s %s", newer.Status, newer.Error)
	}
	h.r.start(old, src, SourceUpload)
	got := h.wait("api", old.ID)
	if got.Status != StatusSkipped || !strings.Contains(got.Error, newer.ID) || got.Image != "" {
		t.Fatalf("older deploy: %s %q image %q", got.Status, got.Error, got.Image)
	}
	if st := h.state("api", ""); st.Live != newer.ID {
		t.Fatalf("live %s, want the newer %s", st.Live, newer.ID)
	}
	if d, _ := h.r.st.getDeploy(ctx, "shop", "api", newer.ID); d.Status != StatusLive {
		t.Fatalf("newer deploy: %s", d.Status)
	}
}

// An app deleted while its first deploy builds never starts: the deploy
// fails before its release command and switch.
func TestAppDeletedWhileBuildingNeverStarts(t *testing.T) {
	h := newHarness(t)
	gated, open := h.gateOn()
	d := h.startDeploy("jobs", "", map[string]string{"index.ts": "work", "BUILD_GATE": ""})
	<-gated
	delete(h.mf.Apps, "jobs")
	h.apply()
	open()
	got := h.wait("jobs", d.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "deleted") {
		t.Fatalf("deploy of a deleted app: %s %q", got.Status, got.Error)
	}
	if st := h.state("jobs", ""); st.Live != "" || len(st.Instances) != 0 {
		t.Fatalf("a deleted app went live: %+v", st)
	}
	if n := h.runningOfApp("jobs"); n != 0 {
		t.Fatalf("%d containers of a deleted app run", n)
	}
}

// An app deleted while its first deploy's instances start (the deletion's
// stop found no state to stop) is stopped by the deploy itself.
func TestAppDeletedWhileStartingIsUndone(t *testing.T) {
	h := newHarness(t)
	gated, open := h.gateOn()
	d := h.startDeploy("jobs", "", map[string]string{"index.ts": "work", "RUN_GATE": ""})
	<-gated // its instance is starting: past every check before the switch
	delete(h.mf.Apps, "jobs")
	h.apply()
	open()
	got := h.wait("jobs", d.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "deleted") {
		t.Fatalf("deploy of a deleted app: %s %q", got.Status, got.Error)
	}
	if st := h.state("jobs", ""); st.Live != "" || len(st.Instances) != 0 {
		t.Fatalf("a deleted app went live: %+v", st)
	}
	if n := h.runningOfApp("jobs"); n != 0 {
		t.Fatalf("%d containers of a deleted app run", n)
	}
}

// A project stopped while a deploy builds keeps its apps down.
func TestProjectStoppedWhileBuildingStaysDown(t *testing.T) {
	h := newHarness(t)
	gated, open := h.gateOn()
	d := h.startDeploy("api", "", map[string]string{"index.ts": "v1", "BUILD_GATE": ""})
	<-gated
	ctx := context.Background()
	plan, err := h.p.Engine.PlanEdit(ctx, "shop", func(cur map[string]change.Resource) (map[string]change.Resource, error) {
		cur[change.KindStopped] = change.Resource{Address: change.KindStopped, Spec: json.RawMessage(`{}`)}
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash}); err != nil {
		t.Fatal(err)
	}
	if err := h.m.Reconcile(ctx, h.p, "shop", change.KindStopped, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	open()
	got := h.wait("api", d.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "stopped") {
		t.Fatalf("deploy in a stopped project: %s %q", got.Status, got.Error)
	}
	if n := h.runningOfApp("api"); n != 0 {
		t.Fatalf("%d containers of a stopped project run", n)
	}
}

// A restart that waited for the environment's lock restarts what is live
// once it has it, not what was live when it was asked.
func TestRestartNeverRevertsANewerRelease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.deploy("api", "", map[string]string{"index.ts": "v2"})
	unlock := h.r.lock(envKey("shop", "api", ""))
	done := make(chan error, 1)
	go func() {
		_, err := h.r.restart(ctx, "shop", "api", "")
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	// Meanwhile v1 goes live (a rollback, under the lock).
	d1, _ := h.r.st.getDeploy(ctx, "shop", "api", v1.ID)
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	err := h.r.promoteLocked(ctx, d1, spec, modeRollback, io.Discard)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := h.state("api", ""); st.Live != v1.ID {
		t.Fatalf("live %s after the restart, want %s (the rollback)", st.Live, v1.ID)
	}
}

// gc chooses and removes builds under the environment's lock: a rollback
// that makes an old build live meanwhile keeps it.
func TestGCNeverRemovesARollbackTarget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var ds []*Deploy
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		ds = append(ds, h.deploy("api", "", map[string]string{"index.ts": v}))
	}
	v2 := ds[1]
	unlock := h.r.lock(envKey("shop", "api", ""))
	done := make(chan struct{})
	go func() {
		h.r.gcKeeping(ctx, "shop", "api", "", 1) // keeps v4 (live) and v3: v2 goes
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	d2, _ := h.r.st.getDeploy(ctx, "shop", "api", v2.ID)
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	err := h.r.promoteLocked(ctx, d2, spec, modeRollback, io.Discard)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	<-done
	got, _ := h.r.st.getDeploy(ctx, "shop", "api", v2.ID)
	if h.state("api", "").Live != v2.ID || got.Image == "" || !h.r.imageExists(ctx, got.Image) {
		t.Fatalf("the live rollback target lost its image: live %s, image %q", h.state("api", "").Live, got.Image)
	}
}

// An image gc could not remove stays in its deploy's record, for the next
// gc (or the hourly sweep) to try again.
func TestGCKeepsImagesItCouldNotRemove(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	v2 := h.deploy("api", "", map[string]string{"index.ts": "v2"})
	h.deploy("api", "", map[string]string{"index.ts": "v3"})
	h.eng.mu.Lock()
	h.eng.rmiFails = true
	h.eng.mu.Unlock()
	// v2 is a rollback target until gc keeps none.
	h.r.gcKeeping(ctx, "shop", "api", "", 0)
	if d, _ := h.r.st.getDeploy(ctx, "shop", "api", v2.ID); d.Image == "" {
		t.Fatal("a failed removal cleared the image from the record")
	}
	h.eng.mu.Lock()
	h.eng.rmiFails = false
	h.eng.mu.Unlock()
	h.r.gcKeeping(ctx, "shop", "api", "", 0)
	if d, _ := h.r.st.getDeploy(ctx, "shop", "api", v2.ID); d.Image != "" || h.r.imageExists(ctx, imageRef("shop", "api", v2.ID)) {
		t.Fatalf("the next gc left the image: %q", d.Image)
	}
}

// When the queue does not say which releases workflow runs are pinned to,
// a replaced release keeps running (without traffic) until it does.
func TestUnknownPinsKeepTheOldReleaseDraining(t *testing.T) {
	h := newHarness(t)
	defer pins.setDown(false)
	ctx := context.Background()
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	pins.setDown(true)
	h.deploy("api", "", map[string]string{"index.ts": "v2"})
	st := h.state("api", "")
	if len(st.Draining) != 1 || st.Draining[0].Release != v1.ID {
		t.Fatalf("v1 stopped though its pins were unknown: %+v", st)
	}
	h.r.reapDrained(ctx)
	if len(h.state("api", "").Draining) != 1 {
		t.Fatal("reaped while the pins were unknown")
	}
	pins.setDown(false)
	h.r.reapDrained(ctx)
	if st := h.state("api", ""); len(st.Draining) != 0 {
		t.Fatalf("not reaped once the queue answered: %+v", st)
	}
}

// A GitHub deploy's answer is a copy: the queue changes its own as it runs
// (here it fails at once: the box has no GitHub App).
func TestGitHubEnqueueAnswersWithACopy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, err := h.r.enqueue(ctx, &ghJob{Project: "shop", App: "api", Repo: "acme/shop", SHA: strings.Repeat("a", 40), Branch: "main", Trigger: "push", By: "tok_test"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, err := h.r.st.getDeploy(ctx, "shop", "api", d.ID)
		if err == nil && cur.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the GitHub deploy never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d.Status != StatusQueued {
		t.Fatalf("the answer changed under the caller: %s", d.Status)
	}
}
