/** The one line that connects Claude Code to this box's MCP endpoint. */
export function mcpCommand() {
  return `claude mcp add --transport http tiffin ${location.origin}/mcp --header "Authorization: Bearer <agent token>"`;
}
