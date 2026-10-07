package email

import (
	"strings"
	"testing"
	"time"
)

// Correlation fields a sender sets itself are dropped: events trust them.
func TestMarkForRelayDropsSendersCorrelation(t *testing.T) {
	raw := []byte("X-PM-Metadata-tiffin-id: msg_OTHER\r\nx-pm-metadata-TIFFIN-ID: msg_OTHER2\r\nX-Tiffin-Message-Id: msg_OTHER3\r\nSubject: x\r\n\r\nb")
	out, _ := markForRelay(raw, "msg_1", "box.test", ProviderPostmark)
	s := string(out)
	if strings.Contains(s, "OTHER") || strings.Count(strings.ToLower(s), "x-pm-metadata-tiffin-id: msg_1\r\n") != 1 ||
		strings.Count(s, "X-Tiffin-Message-Id: msg_1\r\n") != 1 {
		t.Fatalf("postmark: %s", s)
	}
	// A second X-SMTPAPI with a forged unique argument is dropped.
	raw = []byte("X-SMTPAPI: {\"unique_args\":{\"user\":\"7\"}}\r\nX-SMTPAPI: {\"unique_args\":{\"tiffin_id\":\"msg_OTHER\"}}\r\nSubject: x\r\n\r\nb")
	out, _ = markForRelay(raw, "msg_1", "box.test", ProviderSendGrid)
	s = string(out)
	if strings.Contains(s, "OTHER") || strings.Count(s, "X-SMTPAPI") != 1 || !strings.Contains(s, `"tiffin_id":"msg_1"`) || !strings.Contains(s, `"user":"7"`) {
		t.Fatalf("sendgrid: %s", s)
	}
	// One that is not JSON still gets ours instead.
	out, _ = markForRelay([]byte("X-SMTPAPI: nope\r\nSubject: x\r\n\r\nb"), "msg_1", "box.test", ProviderSendGrid)
	if s = string(out); strings.Contains(s, "nope") || !strings.Contains(s, `X-SMTPAPI: {"unique_args":{"tiffin_id":"msg_1"}}`) {
		t.Fatalf("sendgrid, not JSON: %s", s)
	}
}

// An event only changes a message of the project it was sent for.
func TestMatchEventAcrossProjects(t *testing.T) {
	r := newRig(t)
	db := r.p.DB.SQL()
	now := time.Now().UTC()
	add := func(id, project, provider, headerID string, rcpt ...string) {
		t.Helper()
		if err := insertRecord(r.ctx, db, &record{Summary: Summary{ID: id, Project: project, CreatedAt: now, Source: "api", Delivery: DeliveryRelay,
			Status: StatusSent, To: []string{}}, Rcpt: rcpt}); err != nil {
			t.Fatal(err)
		}
		if err := track(r.ctx, db, id, project, provider, headerID); err != nil {
			t.Fatal(err)
		}
	}
	const b, a = "msg_01BBBBBBBBBBBBBBBBBBBBBBBB", "msg_01AAAAAAAAAAAAAAAAAAAAAAAA"
	add(b, "blog", ProviderPostmark, "<news@blog.example>", "Reader@inbox.dev", "attacker@inbox.dev")
	time.Sleep(2 * time.Millisecond)
	// Project shop reuses blog's Message-ID, to the same attacker.
	add(a, "shop", ProviderPostmark, "<news@blog.example>", "attacker@inbox.dev")

	cases := []struct {
		name     string
		provider string
		ev       inEvent
		want     string
	}{
		{"our id", ProviderPostmark, inEvent{TiffinID: b, Event: Event{Recipient: "reader@inbox.dev"}}, b},
		{"our id, a recipient it never had", ProviderPostmark, inEvent{TiffinID: b, Event: Event{Recipient: "else@inbox.dev"}}, ""},
		{"our id, another provider", ProviderSendGrid, inEvent{TiffinID: b, Event: Event{Recipient: "reader@inbox.dev"}}, ""},
		{"shared Message-ID, one project has the recipient", ProviderPostmark, inEvent{HeaderID: "<news@blog.example>", Event: Event{Recipient: "reader@inbox.dev"}}, b},
		{"shared Message-ID, both projects have it", ProviderPostmark, inEvent{HeaderID: "<news@blog.example>", Event: Event{Recipient: "attacker@inbox.dev"}}, ""},
	}
	for _, c := range cases {
		ev := c.ev
		if id, _ := matchEvent(r.ctx, db, c.provider, &ev); id != c.want {
			t.Errorf("%s: matched %q, want %q", c.name, id, c.want)
		}
	}
}

// An event is marked seen only together with its effects: when recording
// the suppression fails, the provider's retry still adds it.
func TestEventEffectsAreAtomic(t *testing.T) {
	r := newRig(t)
	db := r.p.DB.SQL()
	const id = "msg_01CCCCCCCCCCCCCCCCCCCCCCCC"
	if err := insertRecord(r.ctx, db, &record{Summary: Summary{ID: id, Project: "shop", CreatedAt: time.Now(), Source: "api", Delivery: DeliveryRelay,
		Status: StatusSent, To: []string{}}, Rcpt: []string{"ada@inbox.dev"}}); err != nil {
		t.Fatal(err)
	}
	if err := track(r.ctx, db, id, "shop", ProviderPostmark, "<x@y>"); err != nil {
		t.Fatal(err)
	}
	ev := func() []inEvent {
		return []inEvent{{Event: Event{Provider: ProviderPostmark, Type: EventComplained, Recipient: "ada@inbox.dev", At: time.Now()},
			Key: "SpamComplaint/1", TiffinID: id, suppress: "complaint"}}
	}
	if _, err := db.ExecContext(r.ctx, `CREATE TRIGGER fail_sup BEFORE INSERT ON email_suppressions BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.applyEvents(r.ctx, r.p, ProviderPostmark, ev()); err == nil {
		t.Fatal("a failed suppression was ignored")
	}
	if _, err := db.ExecContext(r.ctx, `DROP TRIGGER fail_sup`); err != nil {
		t.Fatal(err)
	}
	if n, err := mod.applyEvents(r.ctx, r.p, ProviderPostmark, ev()); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	sup, _ := suppressed(r.ctx, db, "shop", []string{"ada@inbox.dev"})
	rec, _ := getRecord(r.ctx, db, "shop", id)
	if !sup["ada@inbox.dev"] || rec.Status != StatusComplained {
		t.Fatalf("after the retry: suppressed %v, status %s", sup, rec.Status)
	}
	if n, _ := mod.applyEvents(r.ctx, r.p, ProviderPostmark, ev()); n != 0 {
		t.Fatal("a replay counted again")
	}
}
