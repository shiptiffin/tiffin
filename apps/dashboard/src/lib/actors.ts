// People and agents by name, the same way everywhere: a token called
// "claude-code" is shown as "Claude Code"; anything after a known agent's
// name ("claude-code-opus-5-5", "codex-2") becomes a quieter tag beside it.
// People's names keep their spelling, with a capital first letter.

const AGENTS: Array<[string, string]> = [
  ["claude-code", "Claude Code"],
  ["claude", "Claude"],
  ["codex", "Codex"],
  ["cursor", "Cursor"],
  ["gemini-cli", "Gemini CLI"],
  ["gemini", "Gemini"],
  ["copilot", "Copilot"],
  ["opencode", "opencode"],
  ["aider", "Aider"],
  ["windsurf", "Windsurf"],
  ["devin", "Devin"],
  ["goose", "Goose"],
];

export type ActorName = { name: string; tag?: string };

/** "claude-code" → { name: "Claude Code" }; "claude-code-opus-5-5" → { name: "Claude Code", tag: "opus-5-5" }; "owner" → { name: "Owner" }. */
export function actorName(a: { kind?: string; name?: string; id?: string }): ActorName {
  const raw = (a.name || a.id || "someone").trim();
  if (a.kind !== "agent") return { name: raw.charAt(0).toUpperCase() + raw.slice(1) };
  const lower = raw.toLowerCase();
  for (const [slug, pretty] of AGENTS) {
    if (lower === slug) return { name: pretty };
    if (lower.startsWith(`${slug}-`) || lower.startsWith(`${slug}_`)) return { name: pretty, tag: raw.slice(slug.length + 1) };
  }
  // Unknown agent: words from the slug, keeping a trailing version or number as the tag.
  const m = raw.match(/^(.*?)[-_](v?\d[\w.-]*)$/);
  const base = (m ? m[1] : raw).split(/[-_]+/).filter(Boolean);
  const name = base.map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join(" ") || raw;
  return m ? { name, tag: m[2] } : { name };
}

/** The name alone, for sentences. */
export const actorWords = (a: { kind?: string; name?: string; id?: string }) => actorName(a).name;
