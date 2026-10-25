package change

import (
	"encoding/json"
	"strings"
	"testing"
)

func analyticsOp(action Action, before, after string) Op {
	op := Op{Action: action, Address: KindService + "/analytics"}
	if before != "" {
		op.Before = json.RawMessage(before)
	}
	if after != "" {
		op.After = json.RawMessage(after)
	}
	return op
}

func TestAttributionTurnedOn(t *testing.T) {
	off := `{"retentionDays":365}`
	on := `{"retentionDays":365,"attribution":{"campaigns":true,"signups":true,"firstVisitCookie":"off"}}`
	cookie := `{"retentionDays":365,"attribution":{"campaigns":true,"signups":true,"firstVisitCookie":"outside-eu"}}`
	ask := `{"retentionDays":365,"attribution":{"campaigns":true,"signups":true,"firstVisitCookie":"ask-eu"}}`
	cases := []struct {
		op   Op
		want string
		tier Tier
	}{
		{analyticsOp(Update, off, on), "marketing attribution", TierReversible},
		{analyticsOp(Update, off, cookie), "marketing attribution", TierReversible},
		{analyticsOp(Create, "", on), "marketing attribution", TierReversible},
		{analyticsOp(Update, on, cookie), "the first-visit cookie", TierReversible},
		{analyticsOp(Update, cookie, ask), "the first-visit cookie", TierReversible},
		{analyticsOp(Update, ask, cookie), "", TierReversible},
		{analyticsOp(Update, on, off), "", TierIrreversible},
		{analyticsOp(Update, off, `{"retentionDays":90}`), "", TierIrreversible}, // shorter retention
		{analyticsOp(Delete, on, ""), "", TierIrreversible},
	}
	for i, c := range cases {
		if got := AttributionTurnedOn(&Plan{Ops: []Op{c.op}}); got != c.want {
			t.Errorf("case %d: AttributionTurnedOn = %q, want %q", i, got, c.want)
		}
		if tier, why := Classify(c.op); tier != c.tier || why == "" {
			t.Errorf("case %d: Classify = %s %q, want %s", i, tier, why, c.tier)
		}
	}
	if _, why := Classify(analyticsOp(Update, off, on)); !strings.Contains(why, PrivacyTextVersion) {
		t.Errorf("turning on names the privacy text version: %q", why)
	}
}
