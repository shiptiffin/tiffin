import { describe, expect, test } from "bun:test";
import { agentConnect, mcpCommand } from "@/lib/mcp";

describe("the Claude Code connect line", () => {
  test("a box on a server is reached over HTTP at its /mcp, with the key as a bearer header", () => {
    expect(mcpCommand("tfn_k", "https://dashboard.shop.shiptiffin.app")).toBe(
      'claude mcp add --transport http tiffin https://dashboard.shop.shiptiffin.app/mcp --header "Authorization: Bearer tfn_k"',
    );
  });
  test("a box on this computer runs tiffin mcp", () => {
    expect(mcpCommand("tfn_k", "https://dashboard.tiffin.localhost:8443")).toBe("claude mcp add tiffin -e TIFFIN_TOKEN=tfn_k -- tiffin mcp");
    expect(mcpCommand("tfn_k", "http://127.0.0.1:7070")).toBe("claude mcp add tiffin -e TIFFIN_TOKEN=tfn_k -- tiffin mcp");
  });
});

describe("a new project's Connect your agent", () => {
  test("a box on a server needs an API key and connects over HTTP", () => {
    expect(agentConnect("https://dashboard.new-box.shiptiffin.app")).toEqual({
      needsKey: true,
      cmd: 'claude mcp add --transport http tiffin https://dashboard.new-box.shiptiffin.app/mcp --header "Authorization: Bearer <your key>"',
    });
  });
  test("a box on this computer uses tiffin mcp with the box's own agent key", () => {
    expect(agentConnect("https://dashboard.tiffin.localhost:8443")).toEqual({ needsKey: false, cmd: "claude mcp add tiffin -- tiffin mcp" });
  });
});
