package sentry

import (
	"fmt"
	"strings"
	"testing"
)

func event(line int, value string) string {
	return fmt.Sprintf(`{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","timestamp":1727900000.25,"platform":"node","level":"error",
"exception":{"values":[{"type":"TypeError","value":%q,"stacktrace":{"frames":[
 {"filename":"node:internal/process","function":"processTicks","in_app":false},
 {"filename":"/app/src/cart.ts","function":"addItem","lineno":%d,"in_app":true}]}}]},
"tags":{"route":"/cart"},"request":{"url":"https://shop.example.com/cart"}}`, value, line)
}

func TestEnvelopeAndGrouping(t *testing.T) {
	ev := event(10, "Cannot read properties of undefined (reading 'id')")
	body := `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","dsn":"https://abc123@errors.box.test/4","sent_at":"2026-10-02T00:00:00Z"}` + "\n" +
		fmt.Sprintf(`{"type":"event","length":%d}`, len(ev)) + "\n" + ev + "\n" +
		`{"type":"session"}` + "\n" + `{"sid":"x","status":"ok"}` + "\n" +
		`{"type":"attachment","length":3,"filename":"a.txt"}` + "\n" + "a\nb" + "\n"
	env, err := ParseEnvelope(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Events) != 1 || len(env.Skipped) != 2 || AuthKey("", "", env.DSN) != "abc123" {
		t.Fatalf("%+v", env)
	}
	e1, err := ParseEvent(env.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	if Title(e1) != "TypeError: Cannot read properties of undefined (reading 'id')" || Culprit(e1) != "addItem (/app/src/cart.ts:10)" {
		t.Fatalf("title %q culprit %q", Title(e1), Culprit(e1))
	}
	if e1.Timestamp.Unix() != 1727900000 || e1.Tags["route"] != "/cart" || e1.URL == "" {
		t.Fatalf("%+v", e1)
	}
	// Same bug after an edit moved the line: same group.
	e2, _ := ParseEvent([]byte(event(14, "Cannot read properties of undefined (reading 'id')")))
	if Fingerprint(e1) != Fingerprint(e2) {
		t.Fatal("line moves must not split the group")
	}
	// Messages differing only in numbers group; different texts don't.
	m1, _ := ParseEvent([]byte(`{"message":"user 42 not found"}`))
	m2, _ := ParseEvent([]byte(`{"message":{"formatted":"user 7 not found"}}`))
	m3, _ := ParseEvent([]byte(`{"logentry":{"formatted":"payment failed"}}`))
	if Fingerprint(m1) != Fingerprint(m2) || Fingerprint(m1) == Fingerprint(m3) {
		t.Fatal("message grouping")
	}
	// Explicit fingerprints win.
	f1, _ := ParseEvent([]byte(`{"message":"a","fingerprint":["checkout"]}`))
	f2, _ := ParseEvent([]byte(`{"message":"b","fingerprint":["checkout"]}`))
	if Fingerprint(f1) != Fingerprint(f2) {
		t.Fatal("explicit fingerprint")
	}
	if _, err := ParseEvent([]byte(`{"level":"info"}`)); err == nil {
		t.Fatal("empty event must fail")
	}
}

func TestEnvelopeWithoutLengths(t *testing.T) {
	body := "{}\n{\"type\":\"event\"}\n{\"message\":\"hello\"}\n"
	env, err := ParseEnvelope(strings.NewReader(body))
	if err != nil || len(env.Events) != 1 {
		t.Fatal(err, env)
	}
	if AuthKey("Sentry sentry_version=7, sentry_client=sentry.javascript.bun/8.0.0, sentry_key=k1", "", "") != "k1" {
		t.Fatal("header key")
	}
	if AuthKey("", "k2", "") != "k2" {
		t.Fatal("query key")
	}
}
