import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useEffect, useState } from "react";
import type { Change } from "@/api/client";
import { q } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { splitIntent } from "@/components/ledger-parts";
import { Crumbs, Page, PageHeader, Skeleton } from "@/components/page";
import { Button } from "@/components/ui/button";
import { actorWords } from "@/lib/actors";
import { asTier, intentWords } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { rememberProject } from "@/lib/recent";
import { undoChange } from "@/lib/staged";
import { dayKey, dayLabel, relative } from "@/lib/time";
import { usageQuery, type ProjectUsage } from "@/lib/usage";
import { actorShown } from "@/lib/who";

type LimitEvent = NonNullable<ProjectUsage["limitEvents"]>[number];
type Entry = { change: Change; at: string } | { event: LimitEvent; at: string };

/**
 * History: every change to the project as a plain sentence, who made it
 * ("You", "Claude Code") and when, with Undo where it can be undone; and the
 * moments its limit held it back (an app restarted for memory, connections
 * or cache full), from its usage. No hashes or counts here; the change's own
 * page has the plan and the diff.
 */
export function ProjectHistoryPage({ project }: { project: string }) {
  useTitle(`${project} · History`);
  useEffect(() => rememberProject(project), [project]);
  const changes = useQuery({ ...q.changes(project), placeholderData: (p) => p });
  const usage = useQuery({ ...usageQuery(project), refetchInterval: false });
  const names = useQuery({ ...q.tokenNames, retry: false });
  const { name: me } = useMe();
  const [showUndone, setShowUndone] = useState(false);
  const all = changes.data ?? [];
  const events = usage.data?.limitEvents ?? [];
  const ids = new Set(all.map((c) => c.id));
  const cancelled = (c: Change) => (!!c.undoneBy && ids.has(c.undoneBy)) || (!!c.undoOf && ids.has(c.undoOf));
  const list = showUndone ? all : all.filter((c) => !cancelled(c));
  const hidden = all.length - all.filter((c) => !cancelled(c)).length;
  const entries: Entry[] = [...list.map((c) => ({ change: c, at: c.at })), ...events.map((e) => ({ event: e, at: e.at }))];
  entries.sort((a, b) => (a.at < b.at ? 1 : a.at > b.at ? -1 : 0));
  const days: Array<{ day: string; items: Entry[] }> = [];
  for (const e of entries) {
    const k = dayKey(e.at);
    if (days[days.length - 1]?.day !== k) days.push({ day: k, items: [] });
    days[days.length - 1].items.push(e);
  }
  const who = (c: Change) => {
    const n = c.actor.kind === "agent" ? actorWords(c.actor) : actorShown(c.actor, names.data);
    return me && n === me ? "You" : n;
  };

  return (
    <Page>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "History" }]} />} title="History" />
      <p className="mt-2 text-[0.9375rem] text-ink-2">Everything that changed in {project}, by you or your agents, and when its limit held it back. Most things can be undone.</p>

      {changes.isPending ? (
        <Skeleton className="mt-8 h-48" />
      ) : entries.length === 0 && all.length === 0 ? (
        <p className="mt-8 text-[0.9375rem] text-ink-3">Nothing has changed in {project} yet.</p>
      ) : (
        days.map((d) => (
          <section key={d.day} className="mt-8" aria-label={dayLabel(d.items[0].at)}>
            <h2 className="label mb-1.5">{dayLabel(d.items[0].at)}</h2>
            <ul className="divide-y divide-rule border-y border-rule">
              {d.items.map((item) => {
                if ("event" in item) {
                  const e = item.event;
                  return (
                    <li key={`${e.kind}-${e.at}`} className="py-3">
                      <p className="text-[0.9375rem] leading-[1.375rem] text-ink">{e.message}</p>
                      <p className="mt-0.5 text-[0.8125rem] text-ink-3">
                        Limit reached · {relative(e.at)} ·{" "}
                        <Link to="/projects/$project/usage" params={{ project }} className="underline decoration-rule-3 underline-offset-4 hover:text-ink">
                          Usage
                        </Link>
                      </p>
                    </li>
                  );
                }
                const c = item.change;
                // Undo where it's likely to work: the recent changes that can be undone (older ones open their page).
                const undoable = !c.undoneBy && !c.undoOf && asTier(c.plan.risk) !== "irreversible" && list.indexOf(c) < 5;
                return (
                  <li key={c.id} className="group relative flex items-center gap-3 py-3">
                    <div className="min-w-0 flex-1">
                      <Link
                        to="/changes/$id"
                        params={{ id: c.id }}
                        className={cn("block text-[0.9375rem] leading-[1.375rem] outline-none after:absolute after:inset-0 focus-visible:underline", c.undoneBy ? "text-ink-3" : "text-ink")}
                      >
                        {splitIntent(intentWords(c)).head}
                      </Link>
                      <p className="mt-0.5 text-[0.8125rem] text-ink-3">
                        <span className={cn(c.actor.kind === "agent" && "text-graphite")}>{who(c)}</span> · {relative(c.at)}
                        {c.undoneBy && " · undone"}
                        {asTier(c.plan.risk) === "irreversible" && !c.undoOf && <span className="text-ink-3"> · can’t be undone</span>}
                      </p>
                    </div>
                    {undoable && (
                      <Button variant="ghost" size="sm" className="relative z-10 opacity-70 group-hover:opacity-100 focus-visible:opacity-100" onClick={() => void undoChange(c.id)}>
                        Undo
                      </Button>
                    )}
                    <ChevronRight className="size-4 shrink-0 text-ink-4" aria-hidden />
                  </li>
                );
              })}
            </ul>
          </section>
        ))
      )}
      {hidden > 0 && (
        <button type="button" className="mt-4 text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink" onClick={() => setShowUndone((s) => !s)}>
          {showUndone ? "Hide changes that were undone" : `Show ${words(hidden)} ${hidden === 1 ? "change" : "changes"} that were undone`}
        </button>
      )}
    </Page>
  );
}
