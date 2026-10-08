package page

import (
	"errors"
	"strconv"
	"testing"
)

func TestClamp(t *testing.T) {
	for in, want := range map[int]int{-5: DefaultLimit, 0: DefaultLimit, 1: 1, 50: 50, 200: 200, 201: MaxLimit, 1 << 20: MaxLimit} {
		if got := Clamp(in); got != want {
			t.Errorf("Clamp(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := Encode("2026-10-08T10:00:00Z", "run_01")
	got, err := Decode(c, 2)
	if err != nil || got[0] != "2026-10-08T10:00:00Z" || got[1] != "run_01" {
		t.Fatalf("Decode = %v, %v", got, err)
	}
	if got, err := Decode("", 2); got != nil || err != nil {
		t.Fatalf("empty cursor = %v, %v; want the first page", got, err)
	}
	for _, bad := range []string{"%%%", Encode("only-one"), Encode("a", "b", "c")} {
		if _, err := Decode(bad, 2); !errors.Is(err, ErrBadCursor) {
			t.Errorf("Decode(%q) = %v, want ErrBadCursor", bad, err)
		}
	}
}

func TestMake(t *testing.T) {
	key := func(n int) []string { return []string{strconv.Itoa(n)} }
	p := Make([]int{9, 8, 7}, 2, key)
	if len(p.Items) != 2 || p.NextCursor != Encode("8") {
		t.Fatalf("over the limit: %+v", p)
	}
	p = Make([]int{9, 8}, 2, key)
	if len(p.Items) != 2 || p.NextCursor != "" {
		t.Fatalf("exactly the limit is the last page: %+v", p)
	}
	p = Make[int](nil, 2, key)
	if p.Items == nil || len(p.Items) != 0 || p.NextCursor != "" {
		t.Fatalf("no rows: %+v (items must be [] not null)", p)
	}
}
