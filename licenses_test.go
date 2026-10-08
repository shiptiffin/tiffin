package tiffin

import (
	"strings"
	"testing"
)

func TestLicenses(t *testing.T) {
	got := Licenses("1.2.3", "https://github.com/shiptiffin/tiffin/tree/abc1234")
	for _, want := range []string{
		"Tiffin 1.2.3 is free software",
		"Source code of this version: https://github.com/shiptiffin/tiffin/tree/abc1234",
		"GNU AFFERO GENERAL PUBLIC LICENSE",
		"THIRD-PARTY SOFTWARE IN TIFFIN",
		"github.com/caddyserver/caddy/v2",
		"@fontsource/commit-mono",
		"Instrument Sans, Newsreader and Commit Mono",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Licenses() is missing %q", want)
		}
	}
}
