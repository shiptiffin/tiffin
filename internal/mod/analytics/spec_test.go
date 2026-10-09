package analytics

import (
	"testing"

	"github.com/shiptiffin/tiffin/internal/api"
	_ "github.com/shiptiffin/tiffin/internal/mod/observe"
)

// Both modules register into one spec without schema name clashes, and
// every operation that returns visitor- or app-written data is untrusted.
func TestSpec(t *testing.T) {
	a := api.New(api.Deps{})
	ops := map[string]bool{}
	for _, o := range a.Operations() {
		ops[o.OperationID] = api.IsUntrusted(o)
	}
	for id, untrusted := range map[string]bool{
		"analytics-overview": true, "analytics-realtime": true, "analytics-events": true, "analytics-setup": false,
		"logs-query": true, "metrics-query": true, "issues-list": true, "issue-get": true, "issue-resolve": false,
		"observe-overview": false, "observe-apps": false, "alerts-list": false, "alert-rules-list": false, "alert-rule-put": false,
		"alert-rule-delete": false, "alerts-test": false, "observe-settings-get": false, "observe-settings-set": false, "observe-ingest": false,
	} {
		got, ok := ops[id]
		if !ok {
			t.Errorf("missing operation %s", id)
		} else if got != untrusted {
			t.Errorf("%s: untrusted=%v, want %v", id, got, untrusted)
		}
	}
}
