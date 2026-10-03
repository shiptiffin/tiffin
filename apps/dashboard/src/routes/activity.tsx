import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { CornerUpLeft, Undo2, X } from "lucide-react";
import type { CSSProperties } from "react";
import type { Change, Tier } from "@/api/client";
import { q } from "@/api/queries";
import { ActorMark } from "@/components/actor";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { TiffinMark } from "@/components/logo";
import { OpCounts } from "@/components/op";
import { mcpCommand } from "@/components/palette";
import { ProblemNote } from "@/components/problem";
import { RiskBadge, RiskMark } from "@/components/risk";
import { cn } from "@/lib/cn";
import { asTier, runs, tierCopy, tierRank } from "@/lib/changes";
import { clock, dayKey, dayLabel, full, longDay, relative } from "@/lib/time";

export type ActivitySearch = { project?: string; risk?: Tier };

const words = ["No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten", "Eleven", "Twelve"];
const num = (n: number, cap = true) => {
  const w = n < words.length ? words[n] : String(n);
  return cap ? w : w.toLowerCase();
};
const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

export function ActivityPage({ search }: { search: ActivitySearch }) {
  const { project, risk } = search;
  useTitle(project ? `${project} · Activity` : "Activity");
  const changes = useQuery(q.changes(project));
  const navigate = useNavigate();

  if (changes.isPending) return <Skeleton />;
  if (changes.isError)
    return (
      <Page>
        <ProblemNote error={changes.error} title="Couldn't load the change log" />
      </Page>
    );

  const all = changes.data;
  if (all.length === 0) return project ? <NoChangesIn project={project} /> : <FirstRun />;

  const list = risk ? all.filter((c) => asTier(c.plan.risk) === risk) : all;
  const byTier = (t: Tier) => all.filter((c) => asTier(c.plan.risk) === t).length;
  const agents = all.filter((c) => c.actor.kind === "agent").length;
  const projects = new Set(all.map((c) => c.project)).size;
  const irr = byTier("irreversible");
  const out = byTier("outbound");

  const days: Array<{ key: string; label: string; first: string; items: Change[] }> = [];
  for (const c of list) {
    const k = dayKey(c.at);
    const last = days[days.length - 1];
    if (last && last.key === k) last.items.push(c);
    else days.push({ key: k, label: dayLabel(c.at), first: c.at, items: [c] });
  }

  let i = 0;
  return (
    <Page>
      <header className="animate-rise">
        <h1 className="display text-2xl text-ink sm:text-3xl">
          {num(all.length)} {plural(all.length, "change", "changes")}
          {project ? (
            <>
              {" "}
              to <span className="display-italic">{project}</span>.
            </>
          ) : (
            <>
              {" "}
              across {num(projects, false)} {plural(projects, "project", "projects")}.
            </>
          )}
        </h1>
        <p className="display mt-1 text-xl text-ink-3 sm:text-2xl">
          {agents === 0 ? "All by people." : agents === all.length ? "All by agents." : `Agents made ${num(agents, false)} of them.`}{" "}
          {irr > 0
            ? `${num(irr)} ${plural(irr, "was", "were")} irreversible.`
            : out > 0
              ? `${num(out)} reached outside the box.`
              : "Every one can be undone."}
        </p>

        <div className="mt-8 flex flex-col-reverse gap-6 sm:flex-row sm:items-end sm:justify-between">
          <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Filter by risk">
            {(["reversible", "outbound", "irreversible"] as const).map((t) => {
              const n = byTier(t);
              const on = risk === t;
              return (
                <button
                  key={t}
                  onClick={() => navigate({ to: "/", search: { project, risk: on ? undefined : t } })}
                  aria-pressed={on}
                  title={tierCopy[t].blurb}
                  disabled={n === 0 && !on}
                  className={cn(
                    "group inline-flex h-8 items-center gap-2 rounded-full border px-3 text-sm transition-colors disabled:opacity-40",
                    on ? "border-ink bg-ink text-on-ink" : "border-rule bg-raised/60 text-ink-2 hover:border-rule-strong hover:text-ink",
                  )}
                >
                  <RiskMark tier={t} className={cn(on && "text-on-ink")} />
                  {tierCopy[t].label}
                  <span className={cn("font-mono text-xs tnum", on ? "text-on-ink/70" : "text-ink-4")}>{n}</span>
                </button>
              );
            })}
            {project && (
              <Link
                to="/"
                search={{ risk }}
                className="inline-flex h-8 items-center gap-1.5 rounded-full border border-dashed border-rule-strong px-3 text-sm text-ink-2 hover:text-ink"
              >
                <span className="font-mono">{project}</span>
                <X className="size-3.5" aria-label="Show all projects" />
              </Link>
            )}
          </div>
          <Ledger changes={all} />
        </div>
      </header>

      {list.length === 0 && <p className="mt-12 text-base text-ink-3">No {risk} changes here. That's a good thing.</p>}

      <div className="mt-12 flex flex-col gap-14">
        {days.map((d) => (
          <section key={d.key} aria-labelledby={`day-${d.key}`}>
            <h2 id={`day-${d.key}`} className="mb-4 flex items-baseline gap-3 border-b border-rule pb-2.5">
              <span className="display-italic text-[1.375rem] text-ink">{d.label}</span>
              {d.label === "Today" || d.label === "Yesterday" ? <span className="text-sm text-ink-3">{longDay(d.first)}</span> : null}
              <span className="ml-auto font-mono text-xs text-ink-4 tnum">
                {d.items.length} {plural(d.items.length, "change", "changes")}
              </span>
            </h2>
            <ol className="relative">
              {runs(d.items).map((run, r, all) => {
                const agentRun = run.changes[0].actor.kind === "agent" && run.changes.length > 1;
                const lastRun = r === all.length - 1;
                const rows = run.changes.map((c, k) => (
                  <Row key={c.id} c={c} index={i++} inRun={agentRun} last={lastRun && k === run.changes.length - 1} />
                ));
                if (!agentRun) return rows;
                const a = run.changes[0].actor;
                return (
                  <li key={run.changes[0].id} className="relative my-3 animate-rise rounded-xl border border-rule bg-paper-sunk sm:ml-[3.75rem]">
                    {!lastRun && <span aria-hidden className="absolute top-full left-[calc(1.75rem-1.5px)] hidden h-[15px] w-px bg-rule sm:block" />}
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-rule/70 px-4 py-2.5 text-sm text-ink-3">
                      <ActorMark actor={a} />
                      <span className="font-medium text-ink">{a.name}</span>
                      <span>worked in session</span>
                      <code className="font-mono text-xs text-ink-2">{run.session ?? "(unnamed)"}</code>
                      <span className="ml-auto font-mono text-xs text-ink-4 tnum">
                        {span(run.changes[run.changes.length - 1].at, run.changes[0].at)}
                      </span>
                    </div>
                    <ol className="py-1">{rows}</ol>
                  </li>
                );
              })}
            </ol>
          </section>
        ))}
      </div>
      <p className="mt-16 mb-4 flex items-center justify-center gap-2 text-sm text-ink-4">
        <TiffinMark className="size-4" />
        {list.length >= 200 ? "Showing the latest 200 changes." : "That's everything. The log starts here."}
      </p>
    </Page>
  );
}

function Row({ c, index, inRun, last }: { c: Change; index: number; inRun: boolean; last: boolean }) {
  const tier = asTier(c.plan.risk);
  const undone = !!c.undoneBy;
  const style = { animationDelay: `${Math.min(index, 14) * 28}ms` } as CSSProperties;
  return (
    <li className="group relative animate-rise" style={style}>
      {!inRun && !last && <span aria-hidden className="absolute top-[38px] -bottom-[14px] left-[calc(5.5rem-0.5px)] hidden w-px bg-rule sm:block" />}
      <Link
        to="/changes/$id"
        params={{ id: c.id }}
        className={cn("flex rounded-lg py-3 pr-3 pl-1 transition-colors hover:bg-hover/70", inRun ? "sm:pl-3" : "sm:pl-0")}
      >
        {!inRun && (
          <time
            dateTime={c.at}
            title={full(c.at)}
            className="hidden w-[4.5rem] shrink-0 pt-[3px] pr-3 text-right font-mono text-xs whitespace-nowrap text-ink-3 sm:block"
          >
            {clock(c.at)}
          </time>
        )}
        <span className="relative z-[1] flex w-6 shrink-0 justify-center pt-[2px] sm:w-8">
          <span
            className={cn(
              "grid size-[22px] place-items-center rounded-full transition-colors",
              inRun ? "bg-paper-sunk" : "bg-paper ring-4 ring-paper group-hover:bg-transparent group-hover:ring-transparent",
              tier === "irreversible" && "bg-irr-wash ring-irr-wash",
            )}
          >
            <RiskMark tier={tier} className="size-[15px]" />
          </span>
        </span>
        <span className="min-w-0 flex-1 pl-2.5">
          <span className={cn("block text-md font-medium text-ink", undone && "text-ink-3")}>
            {c.undoOf && <CornerUpLeft className="mr-1.5 mb-0.5 inline size-3.5 text-ink-3" aria-label="Undo:" />}
            <span className={cn(undone && "line-through decoration-ink-4/70")}>{c.intent || <em className="text-ink-3">No intent given</em>}</span>
          </span>
          <span className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-ink-3">
            {!inRun && (
              <span className="inline-flex items-center gap-1.5">
                <ActorMark actor={c.actor} className="size-4 text-[0.55rem]" />
                <span className="text-ink-2">{c.actor.name || c.actor.id}</span>
                {c.actor.session && <code className="font-mono text-xs text-ink-4">{c.actor.session}</code>}
              </span>
            )}
            {!inRun && <Dot />}
            <code className="font-mono text-xs text-ink-2">{c.project}</code>
            <Dot />
            <OpCounts ops={c.plan.ops} />
            <Dot className={cn(!inRun && "sm:hidden")} />
            <time dateTime={c.at} title={full(c.at)} className={cn(!inRun && "sm:hidden", inRun && "font-mono text-xs")}>
              {inRun ? clock(c.at) : relative(c.at)}
            </time>
            {undone && (
              <span className="inline-flex items-center gap-1 rounded-full bg-hover px-1.5 py-px text-xs text-ink-2">
                <Undo2 className="size-3" /> Undone
              </span>
            )}
            {tier !== "reversible" && <RiskBadge tier={tier} size="sm" className="sm:hidden" />}
          </span>
        </span>
        {tier !== "reversible" && (
          <span className="hidden shrink-0 pt-px pl-4 sm:block">
            <RiskBadge tier={tier} size="sm" />
          </span>
        )}
      </Link>
    </li>
  );
}

function Dot({ className }: { className?: string }) {
  return (
    <span aria-hidden className={cn("text-ink-4", className)}>
      ·
    </span>
  );
}

export function Page({ children, className, wide }: { children: React.ReactNode; className?: string; wide?: boolean }) {
  return <div className={cn("mx-auto w-full px-4 pt-10 pb-16 sm:px-8 sm:pt-14", wide ? "max-w-5xl" : "max-w-[52rem]", className)}>{children}</div>;
}

function Skeleton() {
  return (
    <Page>
      <div className="h-9 w-80 max-w-full animate-pulse rounded-md bg-hover" />
      <div className="mt-3 h-7 w-64 max-w-full animate-pulse rounded-md bg-hover/70" />
      <div className="mt-12 space-y-6">
        {[0, 1, 2, 3].map((k) => (
          <div key={k} className="flex gap-4">
            <div className="h-4 w-12 rounded bg-hover/70" />
            <div className="flex-1 space-y-2">
              <div className="h-4 w-3/4 rounded bg-hover" />
              <div className="h-3 w-1/3 rounded bg-hover/70" />
            </div>
          </div>
        ))}
      </div>
    </Page>
  );
}

function NoChangesIn({ project }: { project: string }) {
  return (
    <Page>
      <h1 className="display text-2xl text-ink">Nothing has changed in {project} yet.</h1>
      <p className="mt-2 text-base text-ink-2">
        Changes show up here the moment someone applies a plan.{" "}
        <Link to="/" search={{}} className="text-brass-ink underline underline-offset-4 hover:text-ink">
          See every project
        </Link>
        .
      </p>
    </Page>
  );
}

function FirstRun() {
  return (
    <Page>
      <div className="animate-rise">
        <div className="mb-8 grid size-16 place-items-center rounded-2xl border border-rule bg-raised shadow-pop">
          <TiffinMark className="size-9 text-brass" lid />
        </div>
        <h1 className="display text-3xl text-ink">Your tiffin is empty.</h1>
        <p className="mt-3 max-w-[36rem] text-md text-ink-2">
          Nothing has changed on this box yet. Every change, by you or an agent, is planned first, applied with the plan's hash and written down here,
          so you can always see who did what and put it back.
        </p>
      </div>

      <ol className="mt-10 grid gap-3">
        {[
          { n: "1", t: "Describe your app", d: "Writes a starter tiffin.config.ts in this folder.", cmd: "tiffin init" },
          { n: "2", t: "See what would change", d: "A dry run. Nothing happens, and you get a plan hash.", cmd: "tiffin plan" },
          {
            n: "3",
            t: "Apply exactly that plan",
            d: "Paste the hash. If anything moved since, Tiffin refuses.",
            cmd: 'tiffin apply --confirm <hash> -m "Set up my app"',
          },
        ].map((s, k) => (
          <li
            key={s.n}
            className="animate-rise rounded-xl border border-rule bg-raised/70 p-4 sm:p-5"
            style={{ animationDelay: `${120 + k * 70}ms` }}
          >
            <div className="flex items-baseline gap-3">
              <span className="display-italic text-xl text-brass">{s.n}</span>
              <div className="min-w-0 flex-1">
                <h2 className="text-md font-medium text-ink">{s.t}</h2>
                <p className="mt-0.5 text-base text-ink-3">{s.d}</p>
                <Command cmd={s.cmd} className="mt-3" />
              </div>
            </div>
          </li>
        ))}
      </ol>

      <div className="mt-10 animate-rise rounded-xl border border-dashed border-rule-strong p-5" style={{ animationDelay: "360ms" }}>
        <h2 className="text-md font-medium text-ink">Or let an agent do it</h2>
        <p className="mt-0.5 text-base text-ink-3">
          Give it its own token from{" "}
          <Link to="/tokens" search={{ create: true }} className="text-brass-ink underline underline-offset-4 hover:text-ink">
            Tokens
          </Link>
          , then:
        </p>
        <Command cmd={mcpCommand()} className="mt-3" />
      </div>
    </Page>
  );
}

/**
 * Thirty days as tally marks: one tick per change, coloured by risk, today on
 * the right. The box's handwriting at a glance.
 */
function Ledger({ changes }: { changes: Change[] }) {
  const days = 30;
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const cols: Change[][] = Array.from({ length: days }, () => []);
  for (const c of changes) {
    const d = new Date(c.at);
    d.setHours(0, 0, 0, 0);
    const ago = Math.round((today.getTime() - d.getTime()) / 86_400_000);
    if (ago >= 0 && ago < days) cols[days - 1 - ago].push(c);
  }
  const max = 7;
  const total = cols.reduce((n, c) => n + c.length, 0);
  const tone: Record<Tier, string> = { read: "bg-ink-4", reversible: "bg-rev/70", outbound: "bg-out", irreversible: "bg-irr" };
  return (
    <figure className="shrink-0 select-none" aria-label={`${total} changes in the last 30 days`}>
      <div className="flex h-[48px] items-end gap-[3px]" aria-hidden>
        {cols.map((col, k) => {
          const sorted = [...col].sort((a, b) => (tierRank[a.plan.risk] ?? 4) - (tierRank[b.plan.risk] ?? 4));
          return (
            <div
              key={k}
              className="flex w-[6px] flex-col-reverse gap-px"
              title={col.length ? `${col.length} ${col.length === 1 ? "change" : "changes"}` : undefined}
            >
              {col.length === 0 && <span className="h-[2px] w-full rounded-full bg-rule" />}
              {sorted.slice(0, max).map((c) => (
                <span key={c.id} className={cn("h-[5px] w-full rounded-[1.5px]", tone[asTier(c.plan.risk)], c.undoneBy && "opacity-40")} />
              ))}
            </div>
          );
        })}
      </div>
      <figcaption className="mt-1.5 flex justify-between font-mono text-[0.625rem] text-ink-4">
        <span>30 days</span>
        <span>today</span>
      </figcaption>
    </figure>
  );
}

function span(from: string, to: string) {
  const a = clock(from);
  const b = clock(to);
  return a === b ? a : `${a} – ${b}`;
}
