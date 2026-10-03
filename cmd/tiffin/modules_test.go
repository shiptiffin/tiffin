package main

import (
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mcp"
)

// Every module's operations must coexist: unique IDs, paths, CLI words and
// schema names. This builds the full API and MCP tool list like the binary does.
func TestAllModulesRegister(t *testing.T) {
	a := api.New(api.Deps{})
	seen := map[string]string{}
	for _, o := range a.Operations() {
		cli := ""
		for _, w := range api.CLIPath(o) {
			cli += w + " "
		}
		if prev, ok := seen[cli]; ok {
			t.Errorf("CLI path %q used by %s and %s", cli, prev, o.OperationID)
		}
		seen[cli] = o.OperationID
		if o.Summary == "" || o.Description == "" {
			t.Errorf("%s: missing summary/description", o.OperationID)
		}
	}
	if tools := mcp.Tools(a); len(tools) != len(a.Operations()) {
		t.Fatalf("%d tools for %d operations", len(tools), len(a.Operations()))
	}
	t.Logf("%d operations", len(a.Operations()))
}
