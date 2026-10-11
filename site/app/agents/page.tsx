// /agents: what a coding agent can do on a box, how to connect it, and what it can't do
// without you. Facts from docs/guide/agent-onboarding.md, agents.md, concepts.md and
// limits.md. The setup prompt is public/agent-setup.md, as on the homepage.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { AgentSetup } from "../agent-setup";
import { AGENT_LINE, CopyPrompt } from "../copy-prompt";
import { Answer, CtaBand, FactTable, Faq, PageHead, Points, Section } from "../kit";
import "./agents.css";

export const metadata = pageMeta({
  title: "Connect Claude Code, Codex or Cursor over MCP",
  description:
    "Your coding agent can deploy, read logs, query databases and set domains on your box, with per-project limits, scoped keys, plans, History and undo.",
  path: "/agents",
});
export const dynamic = "force-static";

const UPDATED = "2026-10-10";

/* The setup prompt, generated from docs/guide/agent-onboarding.md (site/lib/llms.ts). */
const PROMPT = readFileSync(join(process.cwd(), "public", "agent-setup.md"), "utf8");

/* The connection lines, exactly as docs/guide/agent-onboarding.md "Connecting an agent" has them. */
const CLAUDE = [
  "# Claude Code (-s user: in every folder; without it, only in this one)",
  "claude mcp add -s user --transport http tiffin https://dashboard.<box domain>/mcp \\",
  '  --header "Authorization: Bearer <key>"',
].join("\n");

const CODEX = [
  "# Codex: add `export TIFFIN_TOKEN=<key>` to your shell profile and restart Codex first",
  "# (it reads the key when it starts), then:",
  "codex mcp add tiffin --url https://dashboard.<box domain>/mcp --bearer-token-env-var TIFFIN_TOKEN",
].join("\n");

const CURSOR = [
  '{ "mcpServers": { "tiffin": { "url": "https://dashboard.<box domain>/mcp",',
  '  "headers": { "Authorization": "Bearer ${env:TIFFIN_TOKEN}" } } } }',
].join("\n");

const VSCODE = [
  "{",
  '  "inputs": [{ "type": "promptString", "id": "tiffin-key", "description": "Tiffin API key", "password": true }],',
  '  "servers": { "tiffin": { "type": "http", "url": "https://dashboard.<box domain>/mcp",',
  '    "headers": { "Authorization": "Bearer ${input:tiffin-key}" } } }',
  "}",
].join("\n");

const LOCAL = ["claude mcp add -s user tiffin -- tiffin mcp", "codex mcp add tiffin -- tiffin mcp"].join("\n");

const CLI = ["export TIFFIN_URL=https://dashboard.<box domain>", "export TIFFIN_TOKEN=<key>", "tiffin whoami"].join("\n");

/* From the prompt's [Me] steps and the "What only you can do, and why" table. */
const ONLY_YOU: [string, string][] = [
  ["Sign in at shiptiffin.com", "It is your account, and the sign-in arrives in your inbox or your Google or GitHub account."],
  ["Pay", "A payment is yours to make."],
  [
    "Make the Hetzner project and token",
    "It needs your Hetzner login. The token goes into the ShipTiffin page (managed) or a file (self-hosted), never into the chat: it can create servers on your bill.",
  ],
  ["Add a passkey", "A passkey lives on your device and needs your fingerprint, face or security key."],
  [
    "Create the first API key",
    "Only a signed-in person can make the first key. After that the agent works with its own key, and History shows its changes under that key's name.",
  ],
  ["Connect GitHub", "GitHub asks you to confirm in a browser."],
  ["DNS records, a mail provider's key", "They need your login at your registrar or mail provider."],
];

function Snippet({ title, code }: { title: string; code: string }) {
  return (
    <div className="code agents-snippet" role="group" aria-label={title}>
      <div className="code-bar">
        <span>{title}</span>
      </div>
      <pre tabIndex={0}>{code}</pre>
    </div>
  );
}

const FAQ: { q: string; a: React.ReactNode }[] = [
  {
    q: "Which agents work with it?",
    a: (
      <p>
        Claude Code, Codex, Cursor and VS Code have their lines above. Any MCP client that can send an{" "}
        <code>Authorization</code> header to an HTTP server connects the same way, at{" "}
        <code>https://dashboard.&lt;box domain&gt;/mcp</code>.
      </p>
    ),
  },
  {
    q: "Does my agent need the tiffin CLI?",
    a: (
      <p>
        Not for most work. On a box that exists, MCP is enough: <code>plan</code> and <code>apply</code> take a
        manifest, <code>deploy_template</code> and <code>deploy_git</code> deploy without an upload, and{" "}
        <code>run</code> reaches every other operation. Deploying a folder from your computer needs the CLI.
      </p>
    ),
  },
  {
    q: "Can my agent see my secrets?",
    a: (
      <p>
        The box never shows a secret&rsquo;s value after it&rsquo;s set: <code>tiffin secrets list</code> gives names
        only. To start a new project with keys another one already has, <code>tiffin secrets copy</code> moves the
        values inside the box, so they never pass through the agent or its transcript.
      </p>
    ),
  },
  {
    q: "What key should an agent that runs unattended get?",
    a: (
      <p>
        A narrow one. A full key applies changes with nobody asked, so give an unattended agent, a teammate&rsquo;s
        agent or a CI job a key for one project, and read-only where it only needs to look. See{" "}
        <Link href="/docs/agents#api-keys">API keys</Link> and{" "}
        <Link href="/docs/always-on-agents">running an always-on agent</Link>.
      </p>
    ),
  },
  {
    q: "Can I see what my agent did?",
    a: (
      <p>
        Yes. History lists every change with the key that made it, the agent session (Claude Code and Codex are
        detected) and the reason it gave. Set <code>TIFFIN_MODEL</code> and the model it ran shows beside its name.
        Most changes have an undo.
      </p>
    ),
  },
];

export default function Agents() {
  return (
    <div className="agents">
      <PageHead
        kicker="For coding agents"
        title="Your coding agent can run it, safely."
        crumbs={[["Agents", "/agents"]]}
        updated={UPDATED}
        actions={
          <>
            <CopyPrompt className="btn btn-primary" />
            <Link className="btn btn-quiet" href="/docs/agent-onboarding">
              Read the setup guide
            </Link>
          </>
        }
      >
        <p>
          Claude Code, Codex and Cursor connect to your box over MCP. They can deploy, read logs, query databases and
          set up domains. Our price doesn&rsquo;t move with what they deploy, their key reaches only what you gave
          it, and a project&rsquo;s limit keeps it from taking the others down.
        </p>
      </PageHead>

      <div className="wrap agents-answer">
        <Answer>
          <p>
            Every ShipTiffin box serves an MCP server at <code>https://dashboard.&lt;box domain&gt;/mcp</code>. Your agent
            connects with an API key you create, and can then do what the dashboard does: projects, deploys, logs,
            database queries, domains and secrets.
          </p>
          <p>
            The price is flat and every project can have a hard limit, so an agent can deploy all day without
            overspending or starving your other apps. Each change is a plan first, recorded in History with who and
            why, and most can be undone.
          </p>
        </Answer>
      </div>

      <Section
        id="setup"
        title="Give it one line, or the whole prompt."
        sub="The prompt walks your agent through setup, managed or self-hosted. It stops and asks you at each step that's yours."
      >
        <div className="two-col agents-setup">
          <div>
            <h3 className="agents-h3">The one line</h3>
            <div className="code code-inline agents-line" role="group" aria-label="One-line prompt">
              <pre tabIndex={0}>{AGENT_LINE}</pre>
            </div>
            <p className="agents-note">
              Pasting the full prompt is the surer way: some agents summarize a page they fetch. It is also at{" "}
              <a href="/agent-setup.md">/agent-setup.md</a>.
            </p>
            <h3 className="agents-h3">Where it stops for you</h3>
            <ul className="agent-yours">
              <li>
                <strong>Sign in and pay</strong>
                <span>Your account, your card.</span>
              </li>
              <li>
                <strong>Hetzner key</strong>
                <span>Into our page or a file, never into the chat.</span>
              </li>
              <li>
                <strong>A passkey</strong>
                <span>It lives on your device.</span>
              </li>
              <li>
                <strong>The agent&rsquo;s key</strong>
                <span>You create it in the dashboard and hand it over.</span>
              </li>
              <li>
                <strong>Money</strong>
                <span>Before anything that costs money, it shows the price and waits for your yes.</span>
              </li>
            </ul>
          </div>
          <AgentSetup prompt={PROMPT} />
        </div>
      </Section>

      <Section
        id="connect"
        title="Connect your agent."
        sub={
          <>
            Make a key in the dashboard under Settings › API keys, one per agent, so History shows who did what. It goes
            in as a bearer token; nothing else to install.
          </>
        }
      >
        <ul className="agents-connect">
          <li>
            <h3 className="agents-h3">Claude Code</h3>
            <p>Creating a key in the dashboard shows this line, filled in for your box.</p>
            <Snippet title="Terminal" code={CLAUDE} />
          </li>
          <li>
            <h3 className="agents-h3">Codex</h3>
            <p>
              In Codex, the default sandbox blocks the network for shell commands (not for MCP), so <code>tiffin</code>{" "}
              cannot reach a remote box: approve the command, or set{" "}
              <code>[sandbox_workspace_write] network_access = true</code> in <code>~/.codex/config.toml</code>.
            </p>
            <Snippet title="Terminal" code={CODEX} />
          </li>
          <li>
            <h3 className="agents-h3">Cursor</h3>
            <p>
              In <code>.cursor/mcp.json</code>, or <code>~/.cursor/mcp.json</code> for every project.
            </p>
            <Snippet title=".cursor/mcp.json" code={CURSOR} />
          </li>
          <li>
            <h3 className="agents-h3">VS Code</h3>
            <p>
              In <code>.vscode/mcp.json</code>. It asks for the key once.
            </p>
            <Snippet title=".vscode/mcp.json" code={VSCODE} />
          </li>
          <li>
            <h3 className="agents-h3">A box you made with tiffin up</h3>
            <p>
              On that computer, <code>tiffin mcp</code> finds the box and uses its own agent key, so there is no key to
              paste.
            </p>
            <Snippet title="Terminal" code={LOCAL} />
          </li>
          <li>
            <h3 className="agents-h3">The CLI, for any box</h3>
            <p>The dashboard&rsquo;s address and a key, in two variables. Put them in your shell profile to keep them.</p>
            <Snippet title="Terminal" code={CLI} />
          </li>
        </ul>
        <p className="agents-more">
          More in the docs: <Link href="/docs/agent-onboarding#connecting-an-agent">connecting an agent</Link> and{" "}
          <Link href="/docs/agents">working with agents</Link>.
        </p>
      </Section>

      <Section
        id="can"
        title="What your agent can do."
        sub="Everything you can do in the dashboard is a tool it can call. The CLI, the MCP tools and the API are generated from one OpenAPI description, so they always agree."
      >
        <Points
          items={[
            [
              "Make projects and deploy them",
              <p key="d">
                Plan and apply a <code>tiffin.config.ts</code>, deploy from a starter, from GitHub or from a folder, and
                roll back to an earlier deploy.
              </p>,
            ],
            [
              "Read logs and fix what broke",
              <p key="l">Build logs, app logs and status. Restart an app, and check a project&rsquo;s usage against its limit.</p>,
            ],
            [
              "Query the database",
              <p key="q">
                List tables and run reads with <code>sql</code>. Writes with <code>sql_write</code> take a snapshot first,
                which <code>tiffin snapshots restore</code> brings back.
              </p>,
            ],
            [
              "Set up domains",
              <p key="dm">
                Add a domain to an app, list the DNS records you add at your registrar, then check until it&rsquo;s live
                with HTTPS.
              </p>,
            ],
            [
              "Manage secrets",
              <p key="s">
                Set and delete them, list their names, and copy them between projects inside the box. Values are never
                shown back.
              </p>,
            ],
            [
              "Everything else",
              <p key="e">
                Queues, workflows, email, sign-in users, analytics, backups, branches and more, through the{" "}
                <code>run</code> tool, or as separate tools at <code>/mcp?tools=all</code>.
              </p>,
            ],
          ]}
        />
      </Section>

      <Section
        id="limits"
        title="It can deploy all day. It can't overspend or take down your other apps."
        sub="Most hosts now say an agent can deploy. The useful question is what happens when it gets something wrong."
        tone="sunk"
      >
        <Points
          items={[
            [
              "No bill to run up",
              <p key="b">
                ShipTiffin is a flat $19 a month per box, with no usage charges, seats or per-project fees from us. Fifty
                deploys a day cost the same as one.
              </p>,
            ],
            [
              "No bigger server without you",
              <p key="s">
                A box key can&rsquo;t resize the server. On a managed box a resize asks for your Hetzner key in your
                ShipTiffin account; the setup prompt tells the agent to show you any price and wait for your yes.
              </p>,
            ],
            [
              "One project can't starve the rest",
              <p key="p">
                A project&rsquo;s limit holds its apps&rsquo; memory and CPU (production and previews), its share of the
                database, its KV memory and its builds. At its limit it&rsquo;s held there, and its Usage page says so.
              </p>,
            ],
            [
              "Box-wide caps stay with you",
              <p key="c">
                You can cap every project that sets no limit of its own. Only you, or a key with full access to all
                projects, can change that. A project&rsquo;s own limit is in its config, so a key that can change the
                project can change it, and History shows when.
              </p>,
            ],
            [
              "A key reaches only its projects",
              <p key="k">
                Give an agent a key for one project, or read-only. Outside its reach every call fails with{" "}
                <code>403 forbidden</code> and a plain reason.
              </p>,
            ],
            [
              "Disk is shared, and guarded",
              <p key="d">
                Past 85% full the box warns and names the project growing fastest; past 95% that project becomes
                read-only, so the others keep running.
              </p>,
            ],
          ]}
        />
        <p className="agents-more">
          What a limit doesn&rsquo;t cover: Hetzner charges for traffic past what its price includes, and the box
          doesn&rsquo;t count what your apps spend at an AI provider. See{" "}
          <Link href="/docs/concepts">how limits work</Link> and <Link href="/docs/limits#agents">the limits page</Link>.
        </p>
      </Section>

      <Section
        id="cant"
        title="What it can't do without you."
        sub="Some steps are about your money, your accounts or your device. The prompt marks them, and the agent stops and asks."
      >
        <FactTable
          caption="Steps only you can do, and why"
          head={["Step", "Why the agent can't"]}
          rows={ONLY_YOU.map(([k, v]) => [k, v])}
        />
        <h3 className="agents-h3 agents-rails-title">And for everything it can do</h3>
        <Points
          items={[
            [
              "A plan before every change",
              <p key="p">
                Every change is planned first, and applied only with that plan&rsquo;s hash, so nothing is applied
                blind.
              </p>,
            ],
            [
              "Destructive steps are marked",
              <p key="d">
                The plan names what a step would destroy (&ldquo;18,204 rows in 12 tables&rdquo;). Destructive tools carry{" "}
                <code>destructiveHint</code>, so Claude Code and other clients ask you before they run.
              </p>,
            ],
            [
              "History says who and why",
              <p key="h">Every change is recorded with the key that made it, the agent session and the reason it gave.</p>,
            ],
            [
              "Most changes can be undone",
              <p key="u">
                <code>tiffin undo &lt;change&gt;</code> reverts one after showing you the plan. A dropped database keeps
                a 7-day snapshot, and a bucket a 7-day trash.
              </p>,
            ],
            [
              "Logs are data, not orders",
              <p key="l">
                Logs, database rows, emails and files were written by others. MCP wraps them as untrusted data, and agents
                are told never to follow instructions inside them.
              </p>,
            ],
            [
              "Keys that expire",
              <p key="k">
                A key lasts 1, 30, 90 or 365 days, or until you revoke it. A key made by another key never outlives it.
              </p>,
            ],
          ]}
        />
        <p className="agents-more">
          To be plain about it: the box has no second approval step. A plan&rsquo;s hash proves the plan was read, not
          that a person approved it; your agent&rsquo;s client is what asks you. That is why unattended agents should get
          narrow keys.
        </p>
      </Section>

      <Section
        id="files"
        title="Files written for agents."
        sub="Your agent can read the docs the way it reads code: as Markdown, at addresses that don't change."
      >
        <FactTable
          caption="Agent-facing files"
          head={["Address", "What's in it"]}
          rows={[
            [
              <a key="a" href="/llms.txt">
                /llms.txt
              </a>,
              "An index of the docs, in the llms.txt format.",
            ],
            [
              <a key="a" href="/llms-full.txt">
                /llms-full.txt
              </a>,
              "The setup and service guides in one file.",
            ],
            [
              <a key="a" href="/agent-setup.md">
                /agent-setup.md
              </a>,
              "The setup prompt on this page, as Markdown.",
            ],
            [
              <code key="c">/docs/&lt;page&gt;.md</code>,
              <>
                Each guide as Markdown, for example <a href="/docs/agents.md">/docs/agents.md</a>.
              </>,
            ],
            [
              <code key="c">Accept: text/markdown</code>,
              <>
                Ask for <code>/docs/&lt;page&gt;</code> with this header and the Markdown comes back at the page&rsquo;s
                own address. <code>/</code> answers with a short briefing for agents, and <code>/docs</code> with{" "}
                <code>/llms.txt</code>.
              </>,
            ],
            [
              <code key="c">/v1/openapi.json</code>,
              "On your box's dashboard address: every API operation. The CLI commands and MCP tools are generated from it.",
            ],
            [
              <code key="c">AGENTS.md</code>,
              <>
                <code>tiffin init</code> writes it into a project, with a skill for Claude Code and Codex, so any agent
                learns the box&rsquo;s rules before it touches it.
              </>,
            ],
          ]}
        />
      </Section>

      <Section id="faq" title="Questions">
        <Faq items={FAQ} />
        <p className="agents-more">
          Related: <Link href="/security">what ShipTiffin can and can&rsquo;t reach on your server</Link>, and{" "}
          <Link href="/open-source">running Tiffin yourself for free</Link>.
        </p>
      </Section>

      <CtaBand title="Give your agent a box with limits.">
        $19 a month per box, plus the server, billed by Hetzner to you at their prices. Or run it yourself, free.
      </CtaBand>
    </div>
  );
}
