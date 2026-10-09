/** A dashboard served from this computer: a box from `tiffin up` on a Mac. */
export function onThisComputer(host: string) {
  return host === "localhost" || host.endsWith(".localhost") || host === "127.0.0.1" || host === "[::1]";
}

/**
 * The one line that connects Claude Code to this box, with an API key from Settings › API keys.
 * A box on a server (a ShipTiffin box, say) is reached over HTTP at its own /mcp, so nothing needs
 * installing first; a box on this computer runs `tiffin mcp`, which finds it by itself.
 */
export function mcpCommand(key = "<your key>", origin = typeof location === "undefined" ? "" : location.origin) {
  if (!origin || onThisComputer(new URL(origin).hostname)) return `claude mcp add tiffin -e TIFFIN_TOKEN=${key} -- tiffin mcp`;
  return `claude mcp add --transport http tiffin ${origin}/mcp --header "Authorization: Bearer ${key}"`;
}

/**
 * How a new project's page connects Claude Code. On this computer `tiffin mcp` uses the box's own
 * agent key, so there is nothing to make first; a box on a server needs an API key, sent over HTTP.
 */
export function agentConnect(origin = typeof location === "undefined" ? "" : location.origin): { needsKey: boolean; cmd: string } {
  if (!origin || onThisComputer(new URL(origin).hostname)) return { needsKey: false, cmd: "claude mcp add tiffin -- tiffin mcp" };
  return { needsKey: true, cmd: mcpCommand(undefined, origin) };
}
