import { Tabs } from "radix-ui";
import { Command } from "@/components/copy";
import { agentConnect, codexCommand, mcpCommand } from "@/lib/mcp";

const tab =
  "h-7 rounded-[6px] px-3 font-[550] text-ink-3 transition-colors hover:text-ink data-[state=active]:bg-paper-raised data-[state=active]:text-ink data-[state=active]:shadow-[var(--top-light),0_0_0_1px_var(--rule-2)]";

/**
 * The lines that connect an agent to this box, for Claude Code or Codex. With a key they use it;
 * with `connect`, the box's own agent key on this computer (see agentConnect).
 */
export function AgentCommand({ secret, connect, wrap, className }: { secret?: string; connect?: boolean; wrap?: boolean; className?: string }) {
  const c = connect ? agentConnect() : { cmd: mcpCommand(secret), codex: codexCommand(secret) };
  return (
    <Tabs.Root defaultValue="claude" className={className}>
      <Tabs.List aria-label="Agent" className="mb-2 inline-flex rounded-[8px] border border-rule-2 bg-paper p-0.5 text-[0.8125rem]">
        <Tabs.Trigger value="claude" className={tab}>
          Claude Code
        </Tabs.Trigger>
        <Tabs.Trigger value="codex" className={tab}>
          Codex
        </Tabs.Trigger>
      </Tabs.List>
      <Tabs.Content value="claude" className="outline-hidden">
        <Command cmd={c.cmd} wrap={wrap} />
      </Tabs.Content>
      <Tabs.Content value="codex" className="outline-hidden">
        {c.codex.map((cmd, i) => (
          <Command key={cmd} cmd={cmd} wrap={wrap} className={i > 0 ? "mt-1.5" : undefined} />
        ))}
        {c.codex.length > 1 && <p className="mt-1.5 text-xs text-ink-3">Codex reads TIFFIN_TOKEN when it starts: put the export in your shell profile (~/.zshrc or ~/.bashrc), then start Codex.</p>}
      </Tabs.Content>
    </Tabs.Root>
  );
}
