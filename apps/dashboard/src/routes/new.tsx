import { Link } from "@tanstack/react-router";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { Page } from "@/components/page";
import { mcpCommand } from "@/lib/mcp";

/**
 * Start a project. This is the boundary for the first-run / new-project flow
 * (pick a starter → name it → the plan tray shows what gets created → apply
 * → the build streams → live URL and "connect your agent"). It is built in a
 * later pass: replace the body of this component, keep the route (/new) and
 * the export name. The Box links here from "New project", "Start a project"
 * and the empty-box placeholder.
 */
export function NewProjectPage() {
  useTitle("New project");
  return (
    <Page>
      <p className="text-[0.8125rem] text-ink-3">
        <Link to="/" className="hover:text-ink">
          Box
        </Link>{" "}
        › New project
      </p>
      <h1 className="sentence mt-2 text-ink">Start a project.</h1>
      <p className="mt-2 max-w-[36rem] text-md text-ink-2">
        A project is one app or a few, with the services they use: a database, a cache, storage, email, sign-in. Picking a starter here comes
        next; for now, start one from your terminal and it appears in the Box.
      </p>
      <ol className="mt-8 max-w-[36rem] divide-y divide-rule border-y border-rule">
        <li className="grid grid-cols-[28px_minmax(0,1fr)] gap-x-3 py-4">
          <span className="ident pt-0.5 text-ink-3">01</span>
          <div>
            <p className="font-[550] text-ink">Write the config</p>
            <p className="mt-0.5 text-sm text-ink-2">In your app’s folder. It asks what you need and writes tiffin.config.ts.</p>
            <Command cmd="tiffin init" className="mt-2.5" />
          </div>
        </li>
        <li className="grid grid-cols-[28px_minmax(0,1fr)] gap-x-3 py-4">
          <span className="ident pt-0.5 text-ink-3">02</span>
          <div>
            <p className="font-[550] text-ink">Review the plan, then apply it</p>
            <p className="mt-0.5 text-sm text-ink-2">The same plan the dashboard shows: every step, its risk and what undo does.</p>
            <Command cmd="tiffin apply" className="mt-2.5" />
          </div>
        </li>
        <li className="grid grid-cols-[28px_minmax(0,1fr)] gap-x-3 py-4">
          <span className="ident pt-0.5 text-ink-3">03</span>
          <div>
            <p className="font-[550] text-ink">Or let your agent do it</p>
            <p className="mt-0.5 text-sm text-ink-2">Connect Claude Code (or any MCP client) and ask it to start a project on this box.</p>
            <Command cmd={mcpCommand()} wrap className="mt-2.5" />
          </div>
        </li>
      </ol>
    </Page>
  );
}
