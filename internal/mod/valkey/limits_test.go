package valkey

import (
	"context"
	"strings"
	"testing"
)

func TestCacheLimit(t *testing.T) {
	// The smaller of maxMemoryMB and the share of the server's maxmemory.
	if got := cacheLimitBytes(64, 25, 400<<20); got != 64<<20 {
		t.Fatalf("maxMemoryMB wins: %d", got)
	}
	if got := cacheLimitBytes(512, 10, 400<<20); got != 40<<20 {
		t.Fatalf("share wins: %d", got)
	}
	if got := cacheLimitBytes(64, 1, 10<<20); got != 1<<20 {
		t.Fatalf("at least 1 MB: %d", got)
	}
	const lim = 100 << 20
	for _, c := range []struct {
		used          int64
		held          bool
		free          int64
		hold, release bool
	}{
		{50 << 20, false, 0, false, false},
		{120 << 20, false, 30 << 20, true, false}, // over: clear down to 90%, and hold if that is not enough
		{120 << 20, true, 30 << 20, false, false}, // already held
		{95 << 20, true, 0, false, false},         // between 90% and 100%: stays held
		{80 << 20, true, 0, false, true},          // under 90%: writes come back
	} {
		free, hold, release := decideCache(c.used, lim, c.held)
		if free != c.free || hold != c.hold || release != c.release {
			t.Errorf("used %d held %v: free %d hold %v release %v", c.used>>20, c.held, free>>20, hold, release)
		}
	}
}

// Over its limit, a project's expiring keys go first, soonest to expire
// first; then its writes are refused, and given back under the limit.
func TestCacheEnforce(t *testing.T) {
	sock := fakeServer(t, map[string]string{
		"AUTH tiffin pw":                "+OK\r\n",
		"SCAN 0 MATCH p_a:* COUNT 1000": "*2\r\n$1\r\n0\r\n*3\r\n$5\r\np_a:x\r\n$5\r\np_a:y\r\n$5\r\np_a:z\r\n",
		"MEMORY USAGE p_a:x SAMPLES 5":  ":4000\r\n",
		"MEMORY USAGE p_a:y SAMPLES 5":  ":4000\r\n",
		"MEMORY USAGE p_a:z SAMPLES 5":  ":4000\r\n",
		"PTTL p_a:x":                    ":-1\r\n",
		"PTTL p_a:y":                    ":9000\r\n",
		"PTTL p_a:z":                    ":10\r\n",
		"UNLINK p_a:z":                  ":1\r\n",
		"UNLINK p_a:y":                  ":1\r\n",
		"ACL SETUSER p_a " + strings.Join(holdRules, " "):    "+OK\r\n",
		"ACL SETUSER p_a " + strings.Join(commandRules, " "): "+OK\r\n",
		"ACL SAVE": "+OK\r\n",
	})
	ctx := context.Background()
	c, err := Dial(ctx, "unix", sock, "tiffin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	keys, used, _, err := prefixUsage(ctx, c, "p_a:")
	if err != nil || keys != 3 || used != 12000 {
		t.Fatalf("usage: %d keys %d bytes %v", keys, used, err)
	}
	// The soonest to expire goes first; keys without an expiry never do.
	freed, n, err := freeExpiring(ctx, c, "p_a:", 3000)
	if err != nil || n != 1 || freed != 4000 {
		t.Fatalf("free one: %d keys %d bytes %v", n, freed, err)
	}
	// Not enough expiring keys: both go, then writes are refused.
	l := &limiter{state: map[string]CacheState{}}
	if err := l.enforce(ctx, c, "a", 2000, CacheState{}); err != nil {
		t.Fatal(err)
	}
	if st := l.state["a"]; !st.WritesRefused || st.LimitBytes != 2000 || st.UsedBytes != 4000 {
		t.Fatalf("held: %+v", st)
	}
	// Under nine tenths of a bigger limit: writes come back.
	if err := l.enforce(ctx, c, "a", 20000, l.state["a"]); err != nil {
		t.Fatal(err)
	}
	if st := l.state["a"]; st.WritesRefused {
		t.Fatalf("released: %+v", st)
	}
}
