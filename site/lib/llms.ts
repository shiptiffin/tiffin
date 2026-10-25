// The files agents read on shiptiffin.com, built from docs/guide:
//
//   /agent-setup.md   the setup prompt from docs/guide/agent-onboarding.md
//   /llms.txt         an index of the docs (https://llmstxt.org)
//   /llms-full.txt    the published guides in one file
//   /docs/<page>.md   each published guide as Markdown
//
// The site deploys from site/ alone, so these are generated into site/public
// and committed: after editing a guide, run `make site-llms` (bun lib/llms-write.ts in site/).
// lib/llms.test.ts fails while they are out of date.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";

export const SITE = "https://shiptiffin.com";

/** The guides published on the site, in reading order, with a line for the index. */
export const PAGES: { name: string; title: string; description: string; note: string; section: Section }[] = [
  { name: "agent-onboarding", title: "Set up ShipTiffin with your coding agent", description: "A prompt that lets Claude Code, Codex or Cursor set up ShipTiffin for you, and the few steps only you can do: paying, Hetzner keys, passkeys.", note: "the setup prompt, what only the person can do, connecting Claude Code, Codex, Cursor and VS Code", section: "Start here" },
  { name: "quickstart", title: "Quickstart", description: "Get a box (managed or self-hosted on Hetzner or any Ubuntu server), connect the tiffin CLI and your coding agent, and ship your first project.", note: "get a box (managed, or self-hosted on Hetzner or any Ubuntu server), connect the CLI and your agent, ship a first project", section: "Start here" },
  { name: "concepts", title: "Concepts", description: "How Tiffin is put together: the box, projects and their resource limits, planned changes with undo, risk tiers, people and API keys.", note: "the box, projects and resources, sharing the box, changes, risk tiers, undo, people and API keys", section: "Start here" },
  { name: "agents", title: "Working with agents", description: "Let AI coding agents run your box safely: the MCP server, plan then apply, scoped API keys, CLI conventions and treating box data as untrusted.", note: "the MCP server, plan then apply, API keys, CLI conventions, untrusted data", section: "Start here" },
  { name: "managed", title: "Managed boxes", description: "How shiptiffin.com sets up a box in your own Hetzner account, what it can and can't do with your key, billing, and what happens if you cancel.", note: "how shiptiffin.com sets up a box in your Hetzner account, what it can and can't do, billing", section: "Start here" },
  { name: "limits", title: "What works and what doesn't", description: "The one honest list of what Tiffin supports, what it supports partly and what it doesn't yet: frameworks, limits and known gaps.", note: "every framework, limit and known gap", section: "Start here" },
  { name: "security", title: "Security model", description: "Tiffin's security model in plain words: what the box protects, how secrets, sign-ins, API keys and updates work, and what to trust it with.", note: "what the box protects and how", section: "Start here" },
  { name: "apps", title: "Apps and deploys", description: "Deploy Next.js, Hono, FastAPI and other apps to your own server: tiffin.config.ts, GitHub deploys, previews per pull request, env and secrets.", note: "frameworks, tiffin.config.ts, deploys, previews, GitHub, env and secrets", section: "Services" },
  { name: "data", title: "Postgres, KV and backups", description: "Postgres 18 per project with branches and extensions, a Redis-compatible KV store, and backups with point-in-time restore to any second of 7 days.", note: "databases, branches, Valkey, backups and restores", section: "Services" },
  { name: "storage", title: "Files", description: "S3-compatible file storage on your own server: private and public buckets, uploads from the browser and on-the-fly image resizing.", note: "S3-compatible buckets, uploads, image resizing", section: "Services" },
  { name: "email", title: "Email", description: "Send email from every app through Resend, Postmark, SendGrid, Amazon SES or any SMTP service, with a test inbox until a provider is connected.", note: "sending through a provider, the test inbox, sending domains", section: "Services" },
  { name: "auth", title: "Sign-in", description: "Sign-in for your apps with Better Auth on your own server: email and password, magic links, passkeys, Google, GitHub and organizations.", note: "Better Auth on the box: passwords, magic links, passkeys, OAuth", section: "Services" },
  { name: "queues", title: "Jobs", description: "Background jobs on your own server: queues, crons and durable workflows, delivered to your app as signed HTTP requests and retried until done.", note: "queues, crons and durable workflows", section: "Services" },
  { name: "domains", title: "Domains", description: "Free HTTPS names for every app, your own domain in two DNS records, a domain per project, and Cloudflare DNS managed for you.", note: "the box's domain and each project's own domains", section: "Services" },
  { name: "search", title: "Search engines", description: "Get apps found on a domain of their own: IndexNow on every deploy, sitemap and robots.txt checks, and which search and AI bots read which pages.", note: "IndexNow, sitemap checks and the crawler log", section: "Services" },
  { name: "observe", title: "Monitoring", description: "Metrics, logs, traces, alerts and Sentry-compatible error tracking for every app on your box, with nothing to install.", note: "metrics, logs, alerts and error tracking", section: "Services" },
  { name: "analytics", title: "Analytics", description: "Cookieless analytics for every app: visits, sources, pages and your own events, counted on your own server with bots filtered out.", note: "cookieless visits and events", section: "Services" },
  { name: "protection", title: "Protection", description: "Rate limits, a proof-of-work bot challenge, CrowdSec and a firewall, on by default for every app on your box.", note: "rate limits, bot challenge, firewall", section: "Services" },
  { name: "moving", title: "Copying and moving", description: "Duplicate a project, export it to one .tiffin file with its code, data and files, import it on another box, or move it in one step.", note: "export, import and moving projects between boxes", section: "More" },
  { name: "always-on-agents", title: "Always-on agents", description: "Run an AI agent of your own on your box, on a schedule, a webhook or a chat message, with durable workflows, approvals and its own secrets.", note: "running an agent of your own on the box", section: "More" },
];

type Section = "Start here" | "Services" | "More";
const SECTIONS: Section[] = ["Start here", "Services", "More"];

const START = "<!-- agent-setup:start -->";
const END = "<!-- agent-setup:end -->";

/** The prompt between the agent-setup markers, without its code fence. */
export function setupPrompt(onboarding: string): string {
  const a = onboarding.indexOf(START);
  const b = onboarding.indexOf(END);
  if (a < 0 || b < a) throw new Error("agent-onboarding.md: no agent-setup markers");
  const lines = onboarding.slice(a + START.length, b).trim().split("\n");
  if (!lines[0].startsWith("```") || !lines[lines.length - 1].startsWith("```")) throw new Error("agent-onboarding.md: the prompt is not fenced");
  return lines.slice(1, -1).join("\n") + "\n";
}

const published = new Set(PAGES.map((p) => p.name));

/** Links between guides, made absolute: to the site's copy. */
function absolutize(md: string): string {
  return md.replace(/\]\(([a-z0-9-]+)\.md(#[^)\s]*)?\)/g, (_, name: string, hash = "") => {
    if (!published.has(name)) throw new Error(`a guide links to ${name}.md, which the site doesn't publish`);
    return `](${SITE}/docs/${name}.md${hash})`;
  });
}

/** A guide as the site publishes it. The managed guide stops before the owner's runbook. */
function page(name: string, md: string): string {
  if (name === "managed") {
    const cut = md.indexOf("\n## Running the control plane");
    if (cut > 0) md = md.slice(0, cut).trimEnd() + "\n";
  }
  return absolutize(md);
}

const SUMMARY =
  "ShipTiffin runs all your apps on one Linux server you own (a box), with a database, auth, KV, file storage, email, jobs, analytics, error tracking and backups already on it. People and their coding agents run it through one CLI (`tiffin`), one MCP server and one dashboard. A managed box is $19 a month per box plus the server at Hetzner; running Tiffin yourself is free (AGPL-3.0).";

function index(): string {
  const out = [
    "# ShipTiffin",
    "",
    `> ${SUMMARY}`,
    "",
    `Setting ShipTiffin up for someone? Follow [the setup steps](${SITE}/agent-setup.md). Some steps only the person can do (signing in, paying, Hetzner credentials, adding a passkey, creating the first API key): stop and ask them at those points.`,
    "",
    "Every box serves an MCP server at `https://dashboard.<box domain>/mcp` (an API key from the dashboard's Settings › API keys, sent as `Authorization: Bearer <key>`) and its API description at `https://dashboard.<box domain>/v1/openapi.json`. The `tiffin` CLI reaches a box with `TIFFIN_URL` and `TIFFIN_TOKEN`; `tiffin <command> --help` lists every command and flag. Change a box only by plan, then apply with the plan's hash.",
    "",
  ];
  for (const section of SECTIONS) {
    out.push(`## ${section}`, "");
    for (const p of PAGES.filter((x) => x.section === section)) out.push(`- [${p.title}](${SITE}/docs/${p.name}.md): ${p.note}`);
    out.push("");
  }
  out.push("## Optional", "");
  out.push(`- [All the guides above in one file](${SITE}/llms-full.txt)`);
  out.push(`- [Security](${SITE}/security): what ShipTiffin does with your Hetzner key and what it can reach, on the website`);
  out.push("");
  return out.join("\n");
}

/** The homepage for agents: what an agent fetching shiptiffin.com needs, as Markdown (served for Accept: text/markdown on /). */
function home(): string {
  return [
    "# ShipTiffin",
    "",
    `> ${SUMMARY}`,
    "",
    "## If you are an agent setting this up for someone",
    "",
    `Follow ${SITE}/agent-setup.md. Some steps only the person can do (signing in, paying, Hetzner credentials, adding a passkey, creating the first API key): stop and ask them at those points. Never ask for passwords or card details.`,
    "",
    "## Price",
    "",
    "- Managed: $19 a month per box ($12 for the first 100 customers, locked for 24 months), monthly, 14-day money-back. No usage charges, seats or per-project fees.",
    "- The server is billed by Hetzner to the person, at Hetzner's prices: about $10 a month before VAT for the smallest (2 vCPU, 4 GB, IPv4, 40 GB data volume).",
    "- Self-hosted: free. The software is open source (AGPL-3.0) at https://github.com/shiptiffin/tiffin.",
    "",
    "## What is on a box",
    "",
    "Apps (Next.js first; Hono, FastAPI, TanStack Start, SvelteKit, Nuxt, React Router, Astro, static sites, Dockerfiles), a preview per pull request, Postgres 18 per project, Redis-compatible KV, S3-compatible files, sign-in (Better Auth), email through the person's own provider, jobs, cron and workflows, cookieless analytics, Sentry-compatible error tracking, logs and metrics, backups with point-in-time restore (7 days), domains with HTTPS. Each project can have a hard limit on CPU and memory.",
    "",
    "## Operating a box",
    "",
    "Every box serves an MCP server at `https://dashboard.<box domain>/mcp` (an API key from Settings › API keys as `Authorization: Bearer <key>`) and its API at `https://dashboard.<box domain>/v1/openapi.json`. Change a box only by plan, then apply with the plan's hash.",
    "",
    "## More",
    "",
    `- [Docs index](${SITE}/llms.txt) and [all docs in one file](${SITE}/llms-full.txt)`,
    `- [Pricing](${SITE}/pricing), [compared with Vercel, Supabase and Coolify](${SITE}/compare), [security](${SITE}/security)`,
    "",
  ].join("\n");
}

/** Every generated file, by its path under site/public. */
export function buildAgentFiles(docsDir: string): Record<string, string> {
  const read = (name: string) => readFileSync(join(docsDir, `${name}.md`), "utf8");
  const files: Record<string, string> = {};
  files["agent-setup.md"] = setupPrompt(read("agent-onboarding"));
  files["llms.txt"] = index();
  files["index.md"] = home();
  const full = ["# ShipTiffin", "", `> ${SUMMARY}`, "", `The setup prompt for coding agents is at ${SITE}/agent-setup.md and in the first guide below.`, ""];
  for (const p of PAGES) {
    const md = page(p.name, read(p.name));
    files[`docs/${p.name}.md`] = md;
    full.push("---", "", `Source: ${SITE}/docs/${p.name}.md`, "", md.trimEnd(), "");
  }
  files["llms-full.txt"] = full.join("\n");
  return files;
}

/** When each published guide last changed: its content hash, and the day that hash first appeared. */
export type DocDates = Record<string, { sha: string; updated: string }>;

export function docDates(docsDir: string, previous: DocDates, today: string): DocDates {
  const out: DocDates = {};
  for (const p of PAGES) {
    const sha = createHash("sha256").update(readFileSync(join(docsDir, `${p.name}.md`))).digest("hex").slice(0, 16);
    out[p.name] = previous[p.name]?.sha === sha ? previous[p.name] : { sha, updated: today };
  }
  return out;
}
