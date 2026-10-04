package protect

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

func TestOwnerSeenAllowlistsOncePerAddress(t *testing.T) {
	var mu sync.Mutex
	var got []string
	done := make(chan struct{}, 10)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	m := &Module{now: func() time.Time { return now }, allow: func(_ context.Context, target string) error {
		mu.Lock()
		got = append(got, target)
		mu.Unlock()
		done <- struct{}{}
		return nil
	}}
	server := &platform.Platform{PublicIP: "203.0.113.5"}
	ctx := context.Background()

	m.OwnerSeen(ctx, &platform.Platform{}, "198.51.100.7") // a local box: never
	m.OwnerSeen(ctx, server, "127.0.0.1")                  // loopback: never
	m.OwnerSeen(ctx, server, "10.1.2.3")                   // private: never
	m.OwnerSeen(ctx, server, "198.51.100.7")
	m.OwnerSeen(ctx, server, "198.51.100.7") // deduplicated
	m.OwnerSeen(ctx, server, "2001:db8:1:2::/64")
	for range 2 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("allowlist not called")
		}
	}
	now = now.Add(7 * time.Hour) // refreshed after a while
	m.OwnerSeen(ctx, server, "198.51.100.7")
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0] != "198.51.100.7/32" && got[1] != "198.51.100.7/32" {
		t.Fatalf("allowlisted %v", got)
	}
}

func TestAllowlistValues(t *testing.T) {
	raw := `name,description,value,comment,expiration,created_at,console_managed
tiffin-owner,Addresses the box's owner connects from (tiffin up and the CLI); never banned,192.168.64.1,owner (2026-10-04),never,2026-10-04T05:40:25Z,false
tiffin-owner,x,2001:db8:1:2::/64,owner,720h,2026-10-04T05:40:25Z,false
`
	got := allowlistValues(raw)
	if len(got) != 2 || got[0] != "192.168.64.1" || got[1] != "2001:db8:1:2::/64" {
		t.Fatalf("%v", got)
	}
}
