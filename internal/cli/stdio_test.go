package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The real binary speaking MCP on stdio, as `claude mcp add tiffin -- tiffin mcp` runs it.
func TestStdioMCPBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "tiffin")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/tiffin").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(), "TIFFIN_HOME="+filepath.Join(t.TempDir(), "box"), "TIFFIN_TOKEN=")
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).
		Connect(t.Context(), &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if cs.InitializeResult().Instructions == "" {
		t.Error("server instructions missing")
	}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	var who map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &who)
	if who["kind"] != "agent" || who["name"] != "local-agent" {
		t.Fatalf("stdio MCP must run as the local agent token, got %v", who)
	}
	m := map[string]any{"project": "demo", "services": map[string]any{"postgres": map[string]any{}}}
	res, _ = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "plan", Arguments: map[string]any{"manifest": m}})
	if res.IsError {
		t.Fatalf("plan: %+v", res.Content)
	}
}
