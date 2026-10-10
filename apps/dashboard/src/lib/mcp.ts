/** A dashboard served from this computer: a box from `tiffin up` on a Mac. */
export function onThisComputer(host: string) {
  return host === "localhost" || host.endsWith(".localhost") || host === "127.0.0.1" || host === "[::1]";
}

const here = () => (typeof location === "undefined" ? "" : location.origin);
const local = (origin: string) => !origin || onThisComputer(new URL(origin).hostname);

/**
 * The one line that connects Claude Code to this box, with an API key from Settings › API keys.
 * A box on a server (a ShipTiffin box, say) is reached over HTTP at its own /mcp, so nothing needs
 * installing first; a box on this computer runs `tiffin mcp`, which finds it by itself.
 * Options go before the name (-e takes several values, so another option ends it), and
 * --scope user adds it for every folder, not just this one.
 */
export function mcpCommand(key = "<your key>", origin = here()) {
  if (local(origin)) return `claude mcp add -e TIFFIN_TOKEN=${key} --scope user tiffin -- tiffin mcp`;
  return `claude mcp add --transport http --scope user tiffin ${origin}/mcp --header "Authorization: Bearer ${key}"`;
}

/**
 * The same for Codex, as lines to run in order. Over HTTP Codex sends the key from TIFFIN_TOKEN,
 * which it reads from its own environment when it starts, so that is exported first.
 */
export function codexCommand(key = "<your key>", origin = here()): string[] {
  if (local(origin)) return [`codex mcp add tiffin --env TIFFIN_TOKEN=${key} -- tiffin mcp`];
  return [`export TIFFIN_TOKEN=${key}`, `codex mcp add tiffin --url ${origin}/mcp --bearer-token-env-var TIFFIN_TOKEN`];
}

/**
 * How a new project's page connects an agent. On this computer `tiffin mcp` uses the box's own
 * agent key, so there is nothing to make first; a box on a server needs an API key, sent over HTTP.
 */
export function agentConnect(origin = here()): { needsKey: boolean; cmd: string; codex: string[] } {
  if (local(origin)) return { needsKey: false, cmd: "claude mcp add --scope user tiffin -- tiffin mcp", codex: ["codex mcp add tiffin -- tiffin mcp"] };
  return { needsKey: true, cmd: mcpCommand(undefined, origin), codex: codexCommand(undefined, origin) };
}
