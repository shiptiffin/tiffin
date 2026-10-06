package manifest

import (
	"encoding/json"
	"testing"
)

func TestDiskJSON(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`["data","x/y"]`, `["data","x/y"]`},                        // the list form stays a list
		{`{"data":"1GB"}`, `["data"]`},                              // default sizes only: a list
		{`{"data":"1024MB","b":"5GB"}`, `{"b":"5GB","data":"1GB"}`}, // sized: an object, every size written
		{`{"data":"2048MB"}`, `{"data":"2GB"}`},
		{`{"data":"1536MB"}`, `{"data":"1536MB"}`},
	} {
		var d Disk
		if err := json.Unmarshal([]byte(c.in), &d); err != nil {
			t.Fatal(err)
		}
		m := &Manifest{Project: "p", Apps: map[string]App{"a": {Disk: d}}}
		Normalize(m)
		got, _ := json.Marshal(m.Apps["a"].Disk)
		if string(got) != c.want {
			t.Errorf("%s: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"": 1 << 30, "500MB": 500 << 20, "5GB": 5 << 30, "2TB": 2 << 40} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"5gb", "0GB", "1.5GB", "5 GB", "5G"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) must fail", in)
		}
	}
}
