package email

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SMTP with a mix of recipients: the suppressed one is recorded, the others relayed.
func TestSMTPSuppressedRecipientsAreLogged(t *testing.T) {
	r := newRig(t)
	s := sinkRelay(t, r, ProviderOther)
	if err := addSuppression(r.ctx, r.p.DB.SQL(), "shop", Suppression{Address: "gone@inbox.dev", Reason: "bounce"}); err != nil {
		t.Fatal(err)
	}
	env, _ := mod.Env(r.ctx, r.p, "shop", "")
	if err := r.smtpSend("shop", env["SMTP_PASSWORD"], "app@shop.test", []string{"gone@inbox.dev", "here@inbox.dev"},
		"To: gone@inbox.dev, here@inbox.dev\r\nSubject: mixed\r\n\r\nhello\r\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "relay delivery", func() bool { return s.count() == 1 })
	s.mu.Lock()
	rcpts := s.rcpts[0]
	s.mu.Unlock()
	if len(rcpts) != 1 || rcpts[0] != "here@inbox.dev" {
		t.Fatalf("relayed to %v", rcpts)
	}
	all := r.inbox(true)
	if len(all) != 1 || all[0].Delivery != DeliveryRelay || len(all[0].Suppressed) != 1 || all[0].Suppressed[0] != "gone@inbox.dev" {
		t.Fatalf("log: %+v", all)
	}
	// Only suppressed recipients: logged, nothing relayed.
	if err := r.smtpSend("shop", env["SMTP_PASSWORD"], "app@shop.test", []string{"gone@inbox.dev"}, "Subject: only\r\n\r\nx\r\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if all := r.inbox(true); s.count() != 1 || len(all) != 2 || all[0].Status != StatusSuppressed {
		t.Fatalf("suppressed only: relayed=%d %+v", s.count(), all[0])
	}
}

// A destroyed project leaves no email behind.
func TestProjectDeletedDropsEmail(t *testing.T) {
	r := newRig(t)
	db := r.p.DB.SQL()
	res, err := Send(r.ctx, r.p, "shop", Message{To: []string{"ada@inbox.dev"}, Subject: "kept?", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_ = addSuppression(r.ctx, db, "shop", Suppression{Address: "gone@inbox.dev", Reason: "bounce"})
	if err := track(r.ctx, db, res.ID, "shop", ProviderSendGrid, "<"+res.ID+"@x>"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(r.ctx, `INSERT INTO email_events(provider, key, project, message, type, recipient, detail, hard, at, received_at)
		VALUES ('sendgrid', 'k1', 'shop', ?, 'delivered', 'ada@inbox.dev', '', 0, ?, ?)`, res.ID, ts(time.Now()), ts(time.Now())); err != nil {
		t.Fatal(err)
	}
	_ = r.p.DB.KVPut(r.ctx, kvNS, "rate/shop", []byte("5"))
	_ = putSending(r.ctx, r.p, &SendingDomain{Project: "shop", Domain: "shop.test", State: SendingVerifying})
	// Another project's mail stays.
	r.apply(`{"project":"cafe","services":{"email":{}}}`)
	other, _ := Send(r.ctx, r.p, "cafe", Message{To: []string{"bo@inbox.dev"}, Subject: "other", Text: "x"})
	if _, err := os.Stat(rawPath(r.p.DataRoot, "shop", res.ID)); err != nil {
		t.Fatal(err)
	}

	if err := mod.ProjectDeleted(r.ctx, r.p, "shop"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM email_messages WHERE project = 'shop'`,
		`SELECT count(*) FROM email_events WHERE project = 'shop'`,
		`SELECT count(*) FROM email_tracking WHERE project = 'shop'`,
		`SELECT count(*) FROM email_suppressions WHERE project = 'shop'`,
	} {
		var n int
		if err := db.QueryRowContext(r.ctx, q).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d %v", strings.TrimPrefix(q, "SELECT count(*) FROM "), n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(messagesDir(r.p.DataRoot), "shop")); !os.IsNotExist(err) {
		t.Errorf("raw files left: %v", err)
	}
	for _, k := range []string{"rate/shop", "sending/shop"} {
		if _, ok, _ := r.p.DB.KVGet(r.ctx, kvNS, k); ok {
			t.Errorf("%s left", k)
		}
	}
	if _, err := getRecord(r.ctx, db, "cafe", other.ID); err != nil {
		t.Fatalf("another project's mail went too: %v", err)
	}
	// Cleaning up twice is fine.
	if err := mod.ProjectDeleted(r.ctx, r.p, "shop"); err != nil {
		t.Fatal(err)
	}
}
