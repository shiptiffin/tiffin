package change_test

import (
	"testing"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/change/changetest"
)

func TestMemStore(t *testing.T) {
	changetest.Run(t, func(*testing.T) change.Store { return change.NewMemStore() })
}
