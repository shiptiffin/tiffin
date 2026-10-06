package release

import "testing"

func TestTrustedKeysParse(t *testing.T) {
	if got := len(TrustedKeys()); got != len(trustedKeys) {
		t.Fatalf("%d of %d trusted keys parse", got, len(trustedKeys))
	}
}
