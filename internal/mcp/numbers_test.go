package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// Structured results carry numbers exactly as the box sent them (through
// float64, 9007199254740993 became 9007199254740992).
func TestResultsKeepNumbers(t *testing.T) {
	res := toResult(200, []byte(`{"id":9007199254740993,"rows":[[9007199254740993]]}`))
	b, _ := json.Marshal(res.StructuredContent)
	if strings.Count(string(b), "9007199254740993") != 2 {
		t.Fatalf("structured content: %s", b)
	}
}
