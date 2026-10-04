//go:build e2e

package e2e

import (
	"log"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// The CLI acts as the local agent when it runs inside an agent session
	// (CLAUDECODE, TIFFIN_AGENT); these tests drive the box as its owner.
	for _, k := range []string{"CLAUDECODE", "TIFFIN_AGENT", "TIFFIN_SESSION", "TIFFIN_MODEL"} {
		os.Unsetenv(k)
	}
	// Remove boxes leaked by earlier crashed runs (older than two hours).
	// TIFFIN_E2E_NO_SWEEP=1 leaves them: other agents' runs share this machine.
	if os.Getenv("TIFFIN_E2E_NO_SWEEP") == "" {
		Sweep(log.Printf, StaleAfter)
	}
	os.Exit(m.Run())
}
