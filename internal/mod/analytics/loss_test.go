package analytics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
)

func TestEstimateLoss(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	var evs []Event
	for i, age := range []int{1, 2, 5, 40, 41, 90} { // days ago
		evs = append(evs, Event{TS: now.AddDate(0, 0, -age), Project: "shop", App: "web", Kind: "pageview", Name: "pageview",
			Host: "shop.box.test", Path: "/", Src: "edge", Visitor: int64(i), Session: int64(i), Props: "{}"})
	}
	evs = append(evs, Event{TS: now, Project: "notes", App: "site", Kind: "pageview", Name: "pageview", Props: "{}"})
	if err := st.Insert(ctx, evs); err != nil {
		t.Fatal(err)
	}
	m := &Module{store: st}
	del := change.Op{Action: change.Delete, Address: "service/analytics"}
	l, err := m.EstimateLoss(ctx, nil, "shop", del)
	if err != nil || l == nil || l.Counts[0].N != 6 || l.Counts[0].Unit != "event" || l.Bytes < 6*rowOverhead {
		t.Fatalf("delete: %+v %v", l, err)
	}
	shorten := change.Op{Action: change.Update, Address: "service/analytics",
		Before: json.RawMessage(`{"retentionDays":365}`), After: json.RawMessage(`{"retentionDays":30}`)}
	if l, err := m.EstimateLoss(ctx, nil, "shop", shorten); err != nil || l == nil || l.Counts[0].N != 3 {
		t.Fatalf("shorten to 30 days: %+v %v", l, err)
	}
	if l, _ := m.EstimateLoss(ctx, nil, "shop", change.Op{Action: change.Delete, Address: "service/postgres"}); l != nil {
		t.Fatalf("not ours: %+v", l)
	}
}
