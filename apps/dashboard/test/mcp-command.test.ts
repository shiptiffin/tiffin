import { describe, expect, test } from "bun:test";
import { agentConnect, codexCommand, mcpCommand } from "@/lib/mcp";

describe("the Claude Code connect line", () => {
  test("a box on a server is reached over HTTP at its /mcp, with the key as a bearer header", () => {
    expect(mcpCommand("tfn_k", "https://dashboard.shop.shiptiffin.app")).toBe(
      'claude mcp add --transport http --scope user tiffin https://dashboard.shop.shiptiffin.app/mcp --header "Authorization: Bearer tfn_k"',
    );
  });
  test("a box on this computer runs tiffin mcp", () => {
    expect(mcpCommand("tfn_k", "https://dashboard.tiffin.localhost:8443")).toBe("claude mcp add -e TIFFIN_TOKEN=tfn_k --scope user tiffin -- tiffin mcp");
    expect(mcpCommand("tfn_k", "http://127.0.0.1:7070")).toBe("claude mcp add -e TIFFIN_TOKEN=tfn_k --scope user tiffin -- tiffin mcp");
  });
});

describe("the Codex connect lines", () => {
  test("a box on a server: the key goes in Codex's environment, then the server is added by URL", () => {
    expect(codexCommand("tfn_k", "https://dashboard.shop.shiptiffin.app")).toEqual([
      "export TIFFIN_TOKEN=tfn_k",
      "codex mcp add tiffin --url https://dashboard.shop.shiptiffin.app/mcp --bearer-token-env-var TIFFIN_TOKEN",
    ]);
  });
  test("a box on this computer runs tiffin mcp", () => {
    expect(codexCommand("tfn_k", "https://dashboard.tiffin.localhost:8443")).toEqual(["codex mcp add tiffin --env TIFFIN_TOKEN=tfn_k -- tiffin mcp"]);
  });
});

describe("a new project's Connect your agent", () => {
  test("a box on a server needs an API key and connects over HTTP", () => {
    expect(agentConnect("https://dashboard.new-box.shiptiffin.app")).toEqual({
      needsKey: true,
      cmd: 'claude mcp add --transport http --scope user tiffin https://dashboard.new-box.shiptiffin.app/mcp --header "Authorization: Bearer <your key>"',
      codex: ["export TIFFIN_TOKEN=<your key>", "codex mcp add tiffin --url https://dashboard.new-box.shiptiffin.app/mcp --bearer-token-env-var TIFFIN_TOKEN"],
    });
  });
  test("a box on this computer uses tiffin mcp with the box's own agent key", () => {
    expect(agentConnect("https://dashboard.tiffin.localhost:8443")).toEqual({
      needsKey: false,
      cmd: "claude mcp add --scope user tiffin -- tiffin mcp",
      codex: ["codex mcp add tiffin -- tiffin mcp"],
    });
  });
});
