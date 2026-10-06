package platform

import (
	"slices"
	"testing"
)

func TestAptWanted(t *testing.T) {
	installed := map[string]string{"git": "1:2.51.0-1", "crowdsec": "1.8.1", "jq": "1.8.1-1"}
	got := aptWanted([]string{"git", "curl", "crowdsec=1.8.1", "jq=1.8.2-1", "nftables=1.1.3"}, func(n string) string { return installed[n] })
	// curl is missing, jq's pin moved, nftables is missing; git and crowdsec stay.
	if want := []string{"curl", "jq=1.8.2-1", "nftables=1.1.3"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
