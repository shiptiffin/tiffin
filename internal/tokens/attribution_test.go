package tokens

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/shiptiffin/tiffin/internal/change"
)

// Only a person turns marketing attribution on: API keys are refused,
// sessions and the owner token pass (sessions are asked to confirm by the
// API, not here). Turning it off is open to keys.
func TestAuthorizerAttribution(t *testing.T) {
	on := &change.Plan{Project: "shop", Risk: change.TierReversible, Ops: []change.Op{{Action: change.Update, Address: "service/analytics",
		Before: json.RawMessage(`{"retentionDays":365}`), After: json.RawMessage(`{"retentionDays":365,"attribution":{}}`)}}}
	off := &change.Plan{Project: "shop", Risk: change.TierIrreversible, Ops: []change.Op{{Action: change.Update, Address: "service/analytics",
		Before: json.RawMessage(`{"retentionDays":365,"attribution":{}}`), After: json.RawMessage(`{"retentionDays":365}`)}}}
	all := []Scope{ScopeRead, ScopePlan, ScopeApplyReversible, ScopeApplyOutbound, ScopeApplyIrreversible}
	key := &Principal{Kind: KindAgent, Scopes: all, Projects: []string{"*"}}
	session := &Principal{Kind: KindHuman, Person: "p1", Scopes: all, Projects: []string{"*"}}
	owner := &Principal{Kind: KindOwner, Scopes: all, Projects: []string{"*"}}
	if err := key.Authorizer()(on); !errors.Is(err, ErrAttributionByKey) || !errors.Is(err, ErrForbidden) {
		t.Fatalf("key turning attribution on: %v", err)
	}
	if err := key.Authorizer()(off); err != nil {
		t.Fatalf("key turning attribution off: %v", err)
	}
	for _, p := range []*Principal{session, owner} {
		if err := p.Authorizer()(on); err != nil {
			t.Fatalf("%s turning attribution on: %v", p.Kind, err)
		}
	}
}
