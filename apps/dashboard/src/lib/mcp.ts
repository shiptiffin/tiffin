/** The one line that connects Claude Code to this box, with an API key from Settings › API keys. */
export function mcpCommand(key = "<your key>") {
  return `claude mcp add tiffin -e TIFFIN_TOKEN=${key} -- tiffin mcp`;
}
