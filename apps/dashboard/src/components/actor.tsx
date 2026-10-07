import type { Actor } from "@/api/client";
import { cn } from "@/lib/cn";

/** Humans are round, agents are square. The owner wears the brass ring. */
export function ActorMark({ actor, className }: { actor: Pick<Actor, "kind" | "name" | "id">; className?: string }) {
  const name = actor.name || actor.id;
  const initial =
    name
      .replace(/[^a-z0-9]/gi, "")
      .slice(0, 1)
      .toUpperCase() || "?";
  const agent = actor.kind === "agent";
  const owner = actor.kind === "owner" || (actor.kind === "human" && name.toLowerCase() === "owner");
  return (
    <span
      aria-hidden
      className={cn(
        "grid [:where(&)]:size-5 shrink-0 place-items-center [:where(&)]:text-[0.625rem] [:where(&)]:leading-none font-semibold select-none",
        agent ? "rounded-[5px] border border-rule-2 bg-paper-sunk font-mono text-ink-2" : "rounded-full bg-ink-2 text-paper",
        owner && "ring-1 ring-brass/80 ring-offset-1 ring-offset-paper",
        className,
      )}
    >
      {initial}
    </span>
  );
}

export function actorKindLabel(kind: string) {
  return kind === "agent" ? "agent" : kind === "system" ? "system" : "human";
}
