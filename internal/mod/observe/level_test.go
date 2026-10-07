package observe

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/levels.json holds the cases. The dashboard's copy of the rule
// (inferLevel in apps/dashboard/src/components/logs-query.ts) must give the
// same answers: keep both in step when the rule changes. "warning" here is
// the dashboard's "warn".
func TestInferLevel(t *testing.T) {
	raw, err := os.ReadFile("testdata/levels.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Line  *string `json:"line"`
		Level string  `json:"level"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range cases {
		if c.Line == nil {
			continue // a note
		}
		n++
		if got := inferLevel(*c.Line); got != c.Level {
			t.Errorf("inferLevel(%q) = %q, want %q", *c.Line, got, c.Level)
		}
	}
	if n < 40 {
		t.Fatalf("only %d cases", n)
	}
}

func TestAppLogRecordLevel(t *testing.T) {
	l := AppLog{Project: "shop", App: "web", Env: "prod", Deploy: "dpl_1", Instance: "1"}
	for _, c := range []struct {
		line, want string
	}{
		{"Error: boom", "error"},
		{"0 errors", ""},
		{`{"log":"FATAL: db gone\n","stream":"stderr","time":"2026-10-02T10:00:00Z"}`, "error"},
		{`{"msg":"ERROR: x","level":"info"}`, "info"},    // a level field wins
		{`{"msg":"warning: disk","other":1}`, "warning"}, // JSON without a level: from the message
		{`{"msg":"listening","level":30}`, "info"},       // pino
		{`{"msg":"boom","level":50}`, "error"},
		{`{"msg":"x","level":"FATAL"}`, "fatal"}, // names stay as written (lowercased)
	} {
		r := appLogRecord([]byte(c.line), l)
		if got, _ := r["level"].(string); got != c.want {
			t.Errorf("%s: level %q, want %q (%v)", c.line, got, c.want, r)
		}
	}
}
