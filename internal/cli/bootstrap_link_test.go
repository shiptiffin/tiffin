package cli

import (
	"strings"
	"testing"
	"time"
)

func TestBootstrapLinkCommand(t *testing.T) {
	env := newEnv(t)
	code, out, errb := run(t, env, "bootstrap-link", "--valid", "24h")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	m := decode(t, out)
	c, _ := m["code"].(string)
	exp, err := time.Parse(time.RFC3339Nano, m["expiresAt"].(string))
	if !strings.HasPrefix(c, "tfl_") || err != nil || time.Until(exp) < 23*time.Hour {
		t.Fatalf("output: %s", out)
	}
	if code, _, _ := run(t, env, "bootstrap-link", "--valid", "48h"); code == 0 {
		t.Fatal("a link for longer than a day must be refused")
	}
}
