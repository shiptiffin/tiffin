package change_test

import (
	"testing"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/change/changetest"
)

func TestMemStore(t *testing.T) {
	changetest.Run(t, func(*testing.T) change.Store { return change.NewMemStore() })
}
