import { actorWords } from "./actors";
import type { Change, Op, Tier } from "@/api/client";

export const tierRank: Record<string, number> = { read: 0, reversible: 1, outbound: 2, irreversible: 3 };

export function asTier(t: string | undefined): Tier {
  return t === "read" || t === "reversible" || t === "outbound" || t === "irreversible" ? t : "irreversible";
}

export const tierCopy: Record<Tier, { label: string; short: string; blurb: string }> = {
  read: { label: "Read only", short: "Read", blurb: "Looks, doesn't touch." },
  reversible: { label: "Reversible", short: "Reversible", blurb: "Can be undone, settings and all." },
  outbound: { label: "Outbound", short: "Outbound", blurb: "Reaches outside the box: people or the internet can see it." },
  irreversible: { label: "Irreversible", short: "Irreversible", blurb: "Destroys data that undo can't bring back." },
};

export function opCounts(ops: Op[] | null | undefined) {
  const c = { create: 0, update: 0, delete: 0 };
  for (const o of ops ?? []) {
    if (o.action === "create") c.create++;
    else if (o.action === "update") c.update++;
    else if (o.action === "delete") c.delete++;
  }
  return c;
}

/** "app/api" → { kind: "app", name: "api" }; "project" → { kind: "project", name: "" }. */
export function splitAddress(address: string) {
  const i = address.indexOf("/");
  return i < 0 ? { kind: address, name: "" } : { kind: address.slice(0, i), name: address.slice(i + 1) };
}

/** The actor as people read it ("Claude Code"); for a person prefer lib/who.ts actorShown, which knows their name. */
export function actorLabel(c: Change): string {
  return actorWords(c.actor);
}

export function isAgent(c: Pick<Change, "actor">) {
  return c.actor.kind === "agent";
}

// ---- field-level diff of spec JSON ----

export type FieldDiff = {
  path: string;
  before?: unknown;
  after?: unknown;
  kind: "added" | "removed" | "changed" | "same";
};

function isObj(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === "object" && !Array.isArray(v);
}

function flatten(v: unknown, prefix: string, out: Map<string, unknown>) {
  if (isObj(v)) {
    const keys = Object.keys(v);
    if (keys.length === 0 && prefix) out.set(prefix, v);
    for (const k of keys.sort()) flatten(v[k], prefix ? `${prefix}.${k}` : k, out);
    return;
  }
  // Arrays of scalars read best whole: ["vector", "pg_trgm"].
  out.set(prefix || "value", v);
}

/** Field-level before/after for one op. Unchanged fields are included (kind "same") for context. */
export function diffOp(op: Op): FieldDiff[] {
  const b = new Map<string, unknown>();
  const a = new Map<string, unknown>();
  if (op.before !== undefined && op.before !== null) flatten(op.before, "", b);
  if (op.after !== undefined && op.after !== null) flatten(op.after, "", a);
  const keys = [...new Set([...b.keys(), ...a.keys()])].sort((x, y) => {
    if (x === "value") return -1;
    if (y === "value") return 1;
    return x.localeCompare(y);
  });
  return keys.map((path) => {
    const hasB = b.has(path);
    const hasA = a.has(path);
    const before = b.get(path);
    const after = a.get(path);
    let kind: FieldDiff["kind"] = "same";
    if (hasB && !hasA) kind = "removed";
    else if (!hasB && hasA) kind = "added";
    else if (JSON.stringify(before) !== JSON.stringify(after)) kind = "changed";
    return { path, before, after, kind };
  });
}

export function formatValue(v: unknown): string {
  if (v === undefined) return "";
  if (typeof v === "string") return `"${v}"`;
  if (isObj(v) && Object.keys(v).length === 0) return "{ }";
  return JSON.stringify(v);
}

export type Run = { session: string | undefined; actorKey: string; changes: Change[] };

/** Consecutive changes by the same actor and session, so a working session reads as one passage. */
export function runs(changes: Change[]): Run[] {
  const out: Run[] = [];
  for (const c of changes) {
    const key = `${c.actor.kind}:${c.actor.id}:${c.actor.session ?? ""}`;
    const last = out[out.length - 1];
    if (last && last.actorKey === key) last.changes.push(c);
    else out.push({ session: c.actor.session, actorKey: key, changes: [c] });
  }
  return out;
}

/** A change's intent as a sentence: capitalised, with a full stop; "undo chg_…: X" becomes "Undid “X”." */
export function intentWords(c: Pick<Change, "intent" | "plan">): string {
  const raw = (c.intent || c.plan.summary || "").trim();
  const undo = raw.match(/^undo chg_[0-9A-Z]+:\s*(.*)$/i);
  // Quote only the undone entry's first sentence: "Undid “Make the uploads bucket public”."
  if (undo) return `Undid “${undo[1].trim().replace(/^(.{12,}?)[.!?]\s+(?=[A-Z0-9“"]).*$/s, "$1").replace(/[.]$/, "")}”.`;
  if (!raw) return raw;
  return raw.charAt(0).toUpperCase() + raw.slice(1) + (/[.!?”]$/.test(raw) ? "" : ".");
}

/** "claude-code (s-7f3a90)" → { name: "claude-code", session: "s-7f3a90" }. */
export function splitRequester(r: string): { name: string; session?: string } {
  const m = r.match(/^(.*?)\s*\(([^)]+)\)$/);
  return m ? { name: m[1], session: m[2] } : { name: r };
}
