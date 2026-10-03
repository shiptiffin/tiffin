import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { Tier } from "@/api/client";
import { cn } from "@/lib/cn";
import { RiskDots } from "./risk-dots";

export type EntryActor = {
  kind: string; // "human" | "owner" | "agent" | "system"
  name: string;
  /** An agent's model or client, if known ("claude-opus-5-5"). */
  model?: string;
  /** An agent's session label. */
  session?: string;
};

/**
 * One signed entry: the shape of every Ledger row, approval permit and
 * receipt. Time in the margin; the actor (people in ink, agents in graphite
 * with their model and session); the intent as one Newsreader sentence;
 * then `+3 ~0 −1`, the risk dots and the signature line.
 *
 *   <SignedEntry time="10:31" actor={{ kind: "human", name: "Bilal" }}
 *     intent="Deployed web v42 to shop." counts={{ create: 0, update: 1, delete: 0 }}
 *     tier="reversible" signature="signed · passkey" to="/changes/$id" params={{ id }} />
 */
export function SignedEntry({
  time,
  timeNote,
  actor,
  intent,
  counts,
  tier,
  signature,
  extra,
  to,
  params,
  muted,
  className,
}: {
  /** The margin: "10:31", "Fri". */
  time: string;
  /** A second line under the time ("asked", "signed"). */
  timeNote?: string;
  actor: EntryActor;
  intent: ReactNode;
  counts?: { create: number; update: number; delete: number };
  tier?: Tier;
  /** The signature line ("signed by Bilal · passkey · 10:46"), drawn after a short rule. */
  signature?: ReactNode;
  /** Anything else for the meta row (a project swatch, "undone"). */
  extra?: ReactNode;
  /** Makes the intent a link. */
  to?: string;
  params?: Record<string, string>;
  /** Undone or superseded: drawn quieter. */
  muted?: boolean;
  className?: string;
}) {
  const agent = actor.kind === "agent";
  const sentence = (
    <span className={cn("entry block", agent ? "text-graphite" : "text-ink", muted && "text-ink-3 line-through decoration-ink-4/60")}>{intent}</span>
  );
  return (
    <article className={cn("grid grid-cols-[44px_minmax(0,1fr)] gap-x-3 py-3", className)}>
      <div className="pt-px text-[0.78125rem] leading-5 text-ink-3 tnum">
        {time}
        {timeNote && <div className="leading-4">{timeNote}</div>}
      </div>
      <div className="min-w-0">
        <p className="truncate text-[0.78125rem] leading-[1.125rem] text-ink-2">
          <b className={cn("font-[550]", agent ? "text-graphite" : "text-ink")}>{actor.name}</b>
          {agent && actor.model && <span className="ident ml-1.5 text-[0.71875rem] text-ink-3">{actor.model}</span>}
          {agent && actor.session && (
            <span className="ml-1.5 text-ink-3">
              session <span className="ident text-[0.71875rem]">{actor.session.slice(0, 4)}</span>
            </span>
          )}
        </p>
        <div className="mt-0.5 mb-1.5">
          {to ? (
            <Link to={to as "/"} params={params as never} className="rounded-xs hover:underline hover:decoration-rule-3 hover:underline-offset-4">
              {sentence}
            </Link>
          ) : (
            sentence
          )}
        </div>
        {(counts || tier || signature || extra) && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-3">
            {counts && <Counts {...counts} />}
            {tier && tier !== "read" && tier !== "reversible" && <RiskDots tier={tier} />}
            {signature && <span className="sigline">{signature}</span>}
            {extra}
          </div>
        )}
      </div>
    </article>
  );
}

/** `+3 ~0 −1` in mono: what a change creates, updates and deletes. */
export function Counts({ create, update, delete: del, className }: { create: number; update: number; delete: number; className?: string }) {
  const cell = (n: number, g: string) => <span data-n={n > 0 ? "" : undefined}>{`${g}${n}`}</span>;
  return (
    <span className={cn("counts inline-flex gap-1.5", className)} aria-label={`${create} to create, ${update} to update, ${del} to delete`}>
      {cell(create, "+")}
      {cell(update, "~")}
      {cell(del, "−")}
    </span>
  );
}
