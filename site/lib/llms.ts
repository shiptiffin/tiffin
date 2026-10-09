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
import { readFileSync } from "node:fs";
import { join } from "node:path";

export const SITE = "https://shiptiffin.com";

/** The guides published on the site, in reading order, with a line for the index. */
export const PAGES: { name: string; title: string; note: string; section: Section }[] = [
  { name: "agent-onboarding", title: "Set up ShipTiffin with your coding agent", note: "the setup prompt, what only the person can do, connecting Claude Code, Codex, Cursor and VS Code", section: "Start here" },
  { name: "quickstart", title: "Quickstart", note: "make a box with tiffin up (a Mac, Hetzner or any Ubuntu server), describe a project, plan, apply, deploy", section: "Start here" },
  { name: "concepts", title: "Concepts", note: "the box, projects and resources, sharing the box, changes, risk tiers, undo, people and API keys", section: "Start here" },
  { name: "agents", title: "Working with agents", note: "the MCP server, plan then apply, API keys, CLI conventions, untrusted data", section: "Start here" },
  { name: "managed", title: "Managed boxes", note: "how shiptiffin.com sets up a box in your Hetzner account, what it can and can't do, billing", section: "Start here" },
  { name: "limits", title: "What works and what doesn't", note: "every framework, limit and known gap", section: "Start here" },
  { name: "security", title: "Security model", note: "what the box protects and how", section: "Start here" },
  { name: "apps", title: "Apps and deploys", note: "frameworks, tiffin.config.ts, deploys, previews, GitHub, env and secrets", section: "Services" },
  { name: "data", title: "Postgres, KV and backups", note: "databases, branches, Valkey, backups and restores", section: "Services" },
  { name: "storage", title: "Files", note: "S3-compatible buckets, uploads, image resizing", section: "Services" },
  { name: "email", title: "Email", note: "sending through a provider, the test inbox, sending domains", section: "Services" },
  { name: "auth", title: "Sign-in", note: "Better Auth on the box: passwords, magic links, passkeys, OAuth", section: "Services" },
  { name: "queues", title: "Jobs", note: "queues, crons and durable workflows", section: "Services" },
  { name: "domains", title: "Domains", note: "the box's domain and each project's own domains", section: "Services" },
  { name: "observe", title: "Monitoring", note: "metrics, logs, alerts and error tracking", section: "Services" },
  { name: "analytics", title: "Analytics", note: "cookieless visits and events", section: "Services" },
  { name: "protection", title: "Protection", note: "rate limits, bot challenge, firewall", section: "Services" },
  { name: "moving", title: "Copying and moving", note: "export, import and moving projects between boxes", section: "More" },
  { name: "always-on-agents", title: "Always-on agents", note: "running an agent of your own on the box", section: "More" },
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
  "ShipTiffin runs all your apps on one Linux server you own (a box), with Postgres, KV, file storage, email, sign-in, jobs, analytics, error tracking and backups already on it. People and their coding agents run it through one CLI (`tiffin`), one MCP server and one dashboard. A managed box is $19 a month per box plus the server at Hetzner; running Tiffin yourself is free (AGPL-3.0).";

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
  out.push(`- [How managed boxes work](${SITE}/managed): the short version, on the website`);
  out.push("");
  return out.join("\n");
}

/** Every generated file, by its path under site/public. */
export function buildAgentFiles(docsDir: string): Record<string, string> {
  const read = (name: string) => readFileSync(join(docsDir, `${name}.md`), "utf8");
  const files: Record<string, string> = {};
  files["agent-setup.md"] = setupPrompt(read("agent-onboarding"));
  files["llms.txt"] = index();
  const full = ["# ShipTiffin", "", `> ${SUMMARY}`, "", `The setup prompt for coding agents is at ${SITE}/agent-setup.md and in the first guide below.`, ""];
  for (const p of PAGES) {
    const md = page(p.name, read(p.name));
    files[`docs/${p.name}.md`] = md;
    full.push("---", "", `Source: ${SITE}/docs/${p.name}.md`, "", md.trimEnd(), "");
  }
  files["llms-full.txt"] = full.join("\n");
  return files;
}
