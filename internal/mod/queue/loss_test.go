package queue

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
)

func TestEstimateLoss(t *testing.T) {
	ctx := context.Background()
	m := &Module{}
	del := change.Op{Action: change.Delete, Address: "queue/emails"}
	if l, err := m.EstimateLoss(ctx, nil, proj, del); l != nil || err != nil {
		t.Fatalf("engine down: %+v %v", l, err)
	}
	e := newEngine(t, nil)
	m.eng = e.Engine
	if err := e.ReconcileQueue(ctx, proj, "emails", json.RawMessage(`{"app":"jobs"}`)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e.send(proj, SendRequest{Name: "emails", Delay: time.Hour, Payload: json.RawMessage(`{"to":"ada@example.com"}`)})
	}
	l, err := m.EstimateLoss(ctx, nil, proj, del)
	if err != nil || l == nil || l.Counts[0].N != 3 || l.Counts[0].Unit != "job" || l.Bytes <= 0 {
		t.Fatalf("got %+v %v", l, err)
	}
	if l, _ := m.EstimateLoss(ctx, nil, proj, change.Op{Action: change.Delete, Address: "topic/emails"}); l != nil {
		t.Fatalf("topics lose nothing: %+v", l)
	}
}
