package email

import (
	"net"
	"os"
	"strconv"
	"testing"
)

// A recipient suppressed while a message waits in the queue (a relay
// outage) is not sent to when delivery resumes.
func TestSuppressionAppliesToQueuedMail(t *testing.T) {
	r := newRig(t)
	db := r.p.DB.SQL()
	host, port, _ := net.SplitHostPort(freeAddr(t)) // nothing listens: every attempt fails for now
	pn, _ := strconv.Atoi(port)
	pw := "relaypass"
	if err := setRelay(r.ctx, r.p, &Relay{Provider: ProviderOther, Host: host, Port: pn, Username: "u", TLS: TLSNone}, &pw); err != nil {
		t.Fatal(err)
	}
	mixed, err := Send(r.ctx, r.p, "shop", Message{To: []string{"Gone@inbox.dev", "here@inbox.dev"}, Subject: "mixed", Text: "x"})
	if err != nil || mixed.Status != StatusQueued {
		t.Fatalf("send: %+v %v", mixed, err)
	}
	only, err := Send(r.ctx, r.p, "shop", Message{To: []string{"gone@inbox.dev"}, Subject: "only", Text: "x"})
	if err != nil || only.Status != StatusQueued {
		t.Fatalf("send: %+v %v", only, err)
	}
	waitFor(t, "a failed attempt", func() bool {
		rec, _ := getRecord(r.ctx, db, "shop", only.ID)
		return rec != nil && rec.Attempts > 0
	})
	if err := addSuppression(r.ctx, db, "shop", Suppression{Address: "gone@inbox.dev", Reason: "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	s := sinkRelay(t, r, ProviderOther)
	mod.kick()
	waitFor(t, "delivery", func() bool { return s.count() == 1 })
	waitFor(t, "the suppressed one done", func() bool {
		rec, _ := getRecord(r.ctx, db, "shop", only.ID)
		return rec.Status == StatusSuppressed
	})
	s.mu.Lock()
	rcpts := s.rcpts[0]
	s.mu.Unlock()
	if len(rcpts) != 1 || rcpts[0] != "here@inbox.dev" {
		t.Fatalf("relayed to %v", rcpts)
	}
	rec, _ := getRecord(r.ctx, db, "shop", mixed.ID)
	if len(rec.Suppressed) != 1 || rec.Suppressed[0] != "Gone@inbox.dev" || rec.Status != StatusSent {
		t.Fatalf("mixed: %+v", rec)
	}
	rec, _ = getRecord(r.ctx, db, "shop", only.ID)
	if rec.Delivery != DeliverySuppressed || len(rec.Suppressed) != 1 || len(rec.Rcpt) != 0 {
		t.Fatalf("only: %+v", rec)
	}
	if _, err := os.Stat(rawPath(r.p.DataRoot, "shop", only.ID)); !os.IsNotExist(err) {
		t.Fatalf("raw file of a message never sent: %v", err)
	}
	if s.count() != 1 {
		t.Fatalf("relayed %d", s.count())
	}
}
