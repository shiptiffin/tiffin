import type { Person, Token } from "@/api/client";
import { actorName } from "./actors";

/** A token with the name a person would recognise: the person behind it, not the token's own label. */
export type NamedToken = Token & { who: string };
export type Names = Map<string, NamedToken>;

/**
 * Token IDs → who they are. The owner token and every dashboard session a
 * person opens resolve to that person's current name ("Bilal", not "owner"
 * or "dashboard session"); agents keep their own name ("Claude Code").
 */
export function whoMap(tokens: Token[], people: Person[]): Names {
  const byId = new Map(tokens.map((t) => [t.id, t] as const));
  const person = new Map(people.map((p) => [p.id, p.name] as const));
  const owner = people.find((p) => p.role === "owner")?.name;
  const resolve = (t: Token, depth = 0): string => {
    if (t.person && person.has(t.person)) return person.get(t.person)!;
    if (t.kind === "owner" && owner) return owner;
    if (t.sponsor && depth < 4 && byId.has(t.sponsor) && t.kind !== "agent") return resolve(byId.get(t.sponsor)!, depth + 1);
    return actorName({ kind: t.kind, name: t.name }).name;
  };
  return new Map(tokens.map((t) => [t.id, { ...t, who: resolve(t) }] as const));
}

/** The name to print for a change's actor: people by who they are now, agents as recorded. */
export function actorShown(a: { kind: string; id: string; name?: string }, names?: Names): string {
  if (a.kind !== "agent") {
    const t = names?.get(a.id);
    if (t) return t.who;
  }
  return a.name || a.id;
}
