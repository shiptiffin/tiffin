import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type Issue, type IssueDetail } from "@/api/modules";
import { q as core } from "@/api/queries";
import type { components } from "@/api/schema";
import { useTitle } from "@/components/favicon";
import { BarStrip } from "@/components/health-chart";
import { Calm, Facts, Group, LevelWord, StateLine } from "@/components/health-kit";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton, Untrusted } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { countWords, int, num, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { full, relative } from "@/lib/time";
import { ProjectIcon } from "@/components/project-icon";

type Frame = components["schemas"]["SentryFrame"];
const states = [
  { v: "unresolved", label: "Open" },
  { v: "resolved", label: "Resolved" },
  { v: "ignored", label: "Ignored" },
] as const;

/** "TypeError: Cannot read properties…" → the type and the message, so the type can be quieter. */
function splitTitle(t: string): { type?: string; message: string } {
  const m = t.match(/^([A-Z][\w.]*(?:Error|Exception|Warning|Panic)?):\s+(.*)$/);
  return m && m[1].length < 40 ? { type: m[1], message: m[2] } : { message: t };
}

/** Errors: exceptions grouped into issues, calmer than Sentry. Open first; resolved and ignored a click away. */
export function ErrorsPage({ project, status = "unresolved" }: { project?: string; status?: string }) {
  useTitle("Errors");
  const navigate = useNavigate();
  const st = (states.some((s) => s.v === status) ? status : "unresolved") as Issue["status"];
  const list = useQuery(mq.issues(project, st));
  const openCount = useQuery({ ...mq.issues(project, "unresolved"), enabled: st !== "unresolved" });
  const projects = useQuery(core.projects);
  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Error tracking" />;
  const issues = list.data ?? [];
  const open = st === "unresolved" ? issues : (openCount.data ?? []);
  const set = (o: { project?: string; status?: string }) => navigate({ to: "/errors", search: { project, status: st === "unresolved" ? undefined : st, ...o } });

  const apps = new Set(open.map((i) => `${i.project}’s ${i.app}`));
  const worst = [...open].sort((a, b) => b.count - a.count)[0];
  const line =
    open.length === 0
      ? "Nothing is going wrong."
      : `${countWords(open.length, "open issue", "open issues", true)}${apps.size === 1 ? `, ${open.length > 1 ? "all " : ""}in ${[...apps][0]}` : ` across ${words(apps.size)} apps`}. ${
          open.length > 1 ? `The worst has happened ${countWords(worst.count, "time")}, last seen ${relative(worst.lastSeen)}.` : `It has happened ${countWords(worst.count, "time")}, last seen ${relative(worst.lastSeen)}.`
        }`;

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: "Health", to: "/status" }]} />}
        title="Errors"
        actions={
          (projects.data?.length ?? 0) > 1 && (
            <select
              value={project ?? ""}
              onChange={(e) => set({ project: e.target.value || undefined })}
              aria-label="Project"
              className="h-8 rounded-[7px] border border-rule-2 bg-paper-raised px-2 text-[0.84375rem] text-ink outline-none focus-visible:border-brass"
            >
              <option value="">Every project</option>
              {(projects.data ?? []).map((p) => (
                <option key={p.name} value={p.name}>
                  {p.name}
                </option>
              ))}
            </select>
          )
        }
      />
      {list.isSuccess && <StateLine>{line}</StateLine>}
      <p className="mt-2 max-w-[40rem] text-[0.875rem] text-ink-3">
        Exceptions your apps report, grouped into issues by where they happen. Any Sentry SDK works: every app already has <code className="ident text-ink-2">SENTRY_DSN</code>.
      </p>

      <nav className="mt-8 flex gap-1 border-b border-rule" aria-label="Which issues">
        {states.map((s) => (
          <button
            key={s.v}
            type="button"
            onClick={() => set({ status: s.v === "unresolved" ? undefined : s.v })}
            aria-current={st === s.v ? "page" : undefined}
            className={cn(
              "relative flex h-10 items-center gap-2 px-3 text-[0.875rem] transition-colors hover:text-ink",
              st === s.v ? "font-[550] text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-[2px] after:rounded-full after:bg-ink" : "text-ink-3",
            )}
          >
            {s.label}
            {s.v === "unresolved" && open.length > 0 && <span className="text-xs font-normal text-ink-3 tnum">{open.length}</span>}
          </button>
        ))}
      </nav>

      <div>
        {list.isPending && <Skeleton className="mt-4 h-40" />}
        {list.isError && <ProblemNote className="mt-6" error={list.error} />}
        {list.isSuccess && issues.length === 0 &&
          (st === "unresolved" ? (
            <Calm art="errors" title={`No open errors${project ? ` in ${project}` : ""}.`} className="mt-4">
              When an app throws, the error lands here, grouped with others like it. Quiet is the goal.
            </Calm>
          ) : (
            <p className="py-8 text-[0.875rem] text-ink-3">{st === "resolved" ? "Nothing resolved yet." : "Nothing ignored."}</p>
          ))}
        {issues.length > 0 && (
          <ul className="divide-y divide-rule border-b border-rule">
            {issues.map((i) => {
              const t = splitTitle(i.title);
              return (
                <li key={i.id}>
                  <Link
                    to="/errors/$id"
                    params={{ id: i.id }}
                    className="group -mx-2 grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-6 rounded-[6px] px-2 py-3.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover sm:grid-cols-[3.25rem_minmax(0,1fr)_7rem]"
                  >
                    <LevelWord level={i.level} className="hidden pt-px text-[0.8125rem] sm:block" />
                    <span className="min-w-0">
                      <span className="block text-[0.9375rem] leading-[1.375rem] text-ink [overflow-wrap:anywhere]">
                        {t.type && <span className="font-[550]">{t.type}: </span>}
                        {t.message}
                      </span>
                      <span className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-[0.8125rem] text-ink-3">
                        <span className="inline-flex items-center gap-1.5">
                          <ProjectIcon project={i.project} size={14} />
                          {i.project} › {i.app}
                        </span>
                        {i.culprit && <span className="ident text-ink-2">{i.culprit}</span>}
                        {i.lastRelease && <span className="ident">{i.lastRelease}</span>}
                        <LevelWord level={i.level} className="sm:hidden" />
                      </span>
                    </span>
                    <span className="text-right">
                      <span className="block text-[1.0625rem] leading-6 text-ink tnum">{num(i.count)}</span>
                      <span className="block text-xs text-ink-3">{st === "resolved" && i.resolvedAt ? `resolved ${relative(i.resolvedAt)}` : `last seen ${relative(i.lastSeen)}`}</span>
                    </span>
                  </Link>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Page>
  );
}

/** One issue: what, where, how often; the stack trace with your code first; resolve or ignore it. */
export function IssuePage({ id }: { id: string }) {
  const qc = useQueryClient();
  const d = useQuery(mq.issue(id));
  useTitle(d.data ? d.data.title : "Error");
  const { can } = useMe();
  const [ev, setEv] = useState(0);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["issue", id] });
    qc.invalidateQueries({ queryKey: ["issues"] });
  };
  const set = useMutation({
    mutationFn: (s: Issue["status"]) => mod.resolveIssue(id, s),
    onSuccess: (_r, s) => {
      refresh();
      if (s === "unresolved") return;
      toast({
        title: s === "resolved" ? "Resolved. It reopens by itself if it happens again." : "Ignored. New events still count, quietly.",
        action: { label: "Undo", run: () => mod.resolveIssue(id, "unresolved").then(refresh) },
      });
    },
  });

  const crumbs = <Crumbs items={[{ label: "Health", to: "/status" }, { label: "Errors", to: "/errors" }]} />;
  if (d.isPending)
    return (
      <Page wide>
        <Skeleton className="h-4 w-32" />
        <Skeleton className="mt-3 h-8 w-2/3" />
      </Page>
    );
  if (d.isError)
    return (
      <Page wide>
        <PageHeader eyebrow={crumbs} title="Error" />
        <ProblemNote className="mt-8" error={d.error} />
      </Page>
    );
  const i: IssueDetail = d.data;
  const events = i.events ?? [];
  const e = events[ev]?.event;
  const t = splitTitle(i.title);

  // Occurrences over the last day, from the events the box kept.
  const now = Math.floor(d.dataUpdatedAt / 1000);
  const from = now - 24 * 3600;
  const buckets = Array.from({ length: 48 }, () => 0);
  for (const x of events) {
    const s = Math.floor(new Date(x.at).getTime() / 1000);
    if (s >= from) buckets[Math.min(47, Math.floor(((s - from) / (24 * 3600)) * 48))]++;
  }
  const sample = events.length < i.count;

  return (
    <Page wide>
      <header>
        <div className="mb-2 text-[0.8125rem] text-ink-3">{crumbs}</div>
        <h1 className="text-[1.375rem] leading-[1.875rem] font-[500] tracking-[-0.015em] text-ink [overflow-wrap:anywhere] sm:text-[1.625rem] sm:leading-8">
          {t.type && <span className="text-ink-2">{t.type}: </span>}
          {t.message}
        </h1>
        <p className="mt-2 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[0.84375rem] text-ink-3">
          <LevelWord level={i.level} />
          <span>in</span>
          <span className="inline-flex items-center gap-1.5 text-ink-2">
            <ProjectIcon project={d.data?.project} size={14} />
            {i.project} › {i.app}
          </span>
          {i.culprit && <span className="ident text-ink-2">{i.culprit}</span>}
          {i.platform && <span>· {i.platform}</span>}
        </p>
        <div className="mt-5 flex flex-wrap items-center gap-2">
          {can("apply:reversible") &&
            (i.status === "unresolved" ? (
              <>
                <Button variant="primary" size="lg" onClick={() => set.mutate("resolved")} disabled={set.isPending}>
                  Resolve
                </Button>
                <Button size="lg" variant="ghost" onClick={() => set.mutate("ignored")} disabled={set.isPending}>
                  Ignore
                </Button>
              </>
            ) : (
              <Button size="lg" onClick={() => set.mutate("unresolved")} disabled={set.isPending}>
                Reopen
              </Button>
            ))}
          <span className="text-[0.84375rem] text-ink-3">
            {i.status === "resolved"
              ? `Resolved${i.resolvedAt ? ` ${relative(i.resolvedAt)}` : ""}. It reopens by itself if it happens again.`
              : i.status === "ignored"
                ? "Ignored: it stays out of the open list and never alerts."
                : "Resolve it when a fix is live; it reopens if it happens again."}
          </span>
        </div>
        {set.isError && <ProblemNote className="mt-3" error={set.error} />}
      </header>

      <div className="mt-10 grid gap-x-12 gap-y-10 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <Group label="Occurrences, last 24 hours" flush aside={`${countWords(i.count, "event")} since ${relative(i.firstSeen)}`}>
          <BarStrip buckets={buckets} from={from} to={now} label="Occurrences in the last 24 hours" />
          {sample && (
            <p className="mt-2 text-xs text-ink-3">
              Drawn from the latest {int(events.length)} of {int(i.count)} events; the box keeps the most recent ones.
            </p>
          )}
        </Group>
        <Group label="Facts" flush>
          <Facts
            narrow
            items={[
              ["Events", int(i.count)],
              ["First seen", <span title={full(i.firstSeen)}>{relative(i.firstSeen)}</span>],
              ["Last seen", <span title={full(i.lastSeen)}>{relative(i.lastSeen)}</span>],
              !!i.lastRelease && ["Release", <span className="ident">{i.lastRelease}</span>],
            ]}
          />
        </Group>
      </div>

      {e && (
        <Group
          label={e.exceptions?.length ? "Stack trace" : "The event"}
          id="trace"
          aside={
            events.length > 1 ? (
              <span className="flex items-center gap-1">
                <Button size="sm" variant="ghost" disabled={ev === 0} onClick={() => setEv(ev - 1)}>
                  ← Newer
                </Button>
                <span className="px-1 tnum">
                  event {ev + 1} of {events.length}
                </span>
                <Button size="sm" variant="ghost" disabled={ev >= events.length - 1} onClick={() => setEv(ev + 1)}>
                  Older →
                </Button>
              </span>
            ) : undefined
          }
        >
          <p className="mb-3 text-[0.84375rem] text-ink-3" title={full(e.timestamp)}>
            {relative(e.timestamp)}
            {e.environment && ` in ${e.environment}`}
            {e.release && (
              <>
                {" · "}
                <span className="ident">{e.release}</span>
              </>
            )}
            {e.transaction && (
              <>
                {" · "}
                <span className="ident text-ink-2">{e.transaction}</span>
              </>
            )}
          </p>
          <Untrusted label="Reported by your app. Shown as plain text.">
            {(e.exceptions ?? []).map((x, xi) => (
              <Exception key={xi} type={x.type} value={x.value} frames={x.frames ?? []} />
            ))}
            {!e.exceptions?.length && e.message && <p className="px-4 py-3 font-mono text-[0.8125rem] text-ink-2">{e.message}</p>}
          </Untrusted>
          {e.tags && Object.keys(e.tags).length > 0 && (
            <p className="label mt-7 mb-2">Tags</p>
          )}
          {e.tags && Object.keys(e.tags).length > 0 && (
            <Facts
              items={Object.entries(e.tags).map(([k, v]) => [<span className="ident">{k}</span>, <span className="ident text-ink-2">{String(v)}</span>] as [ReactNode, ReactNode])}
            />
          )}
        </Group>
      )}
    </Page>
  );
}

/** One exception: its type and message, then frames newest call first; your code in ink, libraries folded away. */
function Exception({ type, value, frames }: { type: string; value: string; frames: Frame[] }) {
  const [libs, setLibs] = useState(false);
  const ordered = [...frames].reverse();
  const hidden = ordered.filter((f) => !f.in_app).length;
  return (
    <div className="border-b border-rule last:border-b-0">
      <p className="px-4 pt-3.5 pb-1 font-mono text-[0.8125rem] leading-5 [overflow-wrap:anywhere]">
        <span className="font-[550] text-danger">{type}</span>
        <span className="text-ink">: {value}</span>
      </p>
      <ol className="pb-2">
        {ordered.map((f, fi) => {
          if (!f.in_app && !libs) return null;
          return (
            <li key={fi} className={cn("px-4 py-1.5 font-mono text-[0.75rem] leading-5", !f.in_app && "text-ink-3")}>
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span className={f.in_app ? "font-[550] text-ink" : undefined}>{f.function ?? "(anonymous)"}</span>
                <span className="text-ink-3 [overflow-wrap:anywhere]">
                  {f.filename}
                  {f.lineno ? `:${f.lineno}` : ""}
                  {f.colno ? `:${f.colno}` : ""}
                </span>
                {!f.in_app && <span className="font-sans text-[0.6875rem] text-ink-4">library</span>}
              </span>
              {f.context_line && (
                <pre className="mt-1 overflow-x-auto rounded-[6px] bg-paper-sunk py-1.5 pr-3 whitespace-pre text-ink shadow-[inset_2px_0_0_var(--danger-rule)]">
                  {f.lineno && <span className="inline-block w-12 pr-3 text-right text-ink-4 select-none">{f.lineno}</span>}
                  {f.context_line.replace(/^\s+/, (m) => m.slice(0, Math.min(m.length, 2)))}
                </pre>
              )}
            </li>
          );
        })}
      </ol>
      {hidden > 0 && (
        <button type="button" onClick={() => setLibs(!libs)} className="mb-3 ml-4 text-[0.8125rem] text-ink-3 underline-offset-4 hover:text-ink hover:underline">
          {libs ? "Hide library frames" : `Show ${countWords(hidden, "library frame")}`}
        </button>
      )}
    </div>
  );
}
