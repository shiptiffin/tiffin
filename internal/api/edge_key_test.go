package api_test

import (
	"os"
	"testing"

	"github.com/shiptiffin/tiffin/internal/api"
)

// testEdgeKey stands in for the edge's key: requests that carry it with
// X-Forwarded-For from loopback come "through the edge".
const testEdgeKey = "test-edge-key"

func TestMain(m *testing.M) {
	api.SetEdgeKey(testEdgeKey)
	os.Exit(m.Run())
}
