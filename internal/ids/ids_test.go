package ids

import (
	"regexp"
	"sort"
	"testing"
	"time"
)

func TestFormatAndOrder(t *testing.T) {
	re := regexp.MustCompile(`^chg_[0-9A-HJKMNP-TV-Z]{26}$`)
	var got []string
	base := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	for i := range 100 {
		id := "chg_" + ulid(base.Add(time.Duration(i)*time.Millisecond))
		if !re.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
		got = append(got, id)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("ids from increasing times must sort in order")
	}
	// Known value: the timestamp prefix of the Unix epoch is all zeros.
	if p := ulid(time.UnixMilli(0))[:10]; p != "0000000000" {
		t.Fatalf("epoch prefix %q", p)
	}
	if a, b := New("x"), New("x"); a == b {
		t.Fatal("ids must be unique")
	}
}
