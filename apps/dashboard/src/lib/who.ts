import type { Person, Token } from "@/api/client";
import { actorName } from "./actors";

/** A key (or the owner) with the name a person would recognise. */
export type NamedToken = { id: string; name: string; who: string };
export type Names = Map<string, NamedToken>;
const OWNER = "__owner";

/**
 * Key IDs → who they are: an API key by its own name ("Claude Code"), and the
 * box's owner (changes signed "owner") by the owner's current name ("Sam").
 */
export function whoMap(tokens: Token[], people: Person[]): Names {
  const owner = people.find((p) => p.role === "owner")?.name;
  const m: Names = new Map(tokens.map((t) => [t.id, { id: t.id, name: t.name, who: t.name === "owner" && owner ? owner : actorName({ kind: "agent", name: t.name }).name }] as const));
  if (owner) m.set(OWNER, { id: OWNER, name: "owner", who: owner });
  return m;
}

/** The name to print for a change's actor: the owner by their name, people and agents as recorded. */
export function actorShown(a: { kind: string; id: string; name?: string }, names?: Names): string {
  if (a.kind !== "agent") {
    if ((a.name ?? "").toLowerCase() === "owner" || a.kind === "owner") {
      const o = names?.get(OWNER);
      if (o) return o.who;
    }
    const t = names?.get(a.id);
    if (t && a.kind !== "human") return t.who;
  }
  return a.name || a.id;
}
