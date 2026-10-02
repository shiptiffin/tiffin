//go:build e2e

package e2e

import (
	"log"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Remove boxes leaked by earlier crashed runs (older than two hours).
	Sweep(log.Printf, StaleAfter)
	os.Exit(m.Run())
}
