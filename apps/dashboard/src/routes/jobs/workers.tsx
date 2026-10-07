// Workers: which app processes what, and whether it's there to do it. One
// row per app that receives jobs (worker apps, and web apps a queue, a
// schedule or a workflow delivers to): its instances, when it last did
// something, and its queues, schedules and workflows with their load. Work
// that waits on an app that isn't running is said first. Addresses outside
// the box are listed after, with how their calls are going.
import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight } from "lucide-react";
import { notOnBox } from "@/api/client";
import { jq } from "@/api/jobs";
import { mod3, type AppRuntime, type QueueCron, type QueueStats } from "@/api/modules";
import { q as api } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { JobsStart, JobsTrouble, likelyApp } from "@/components/jobs-start";
import { cronHuman, StateSentence } from "@/components/jobs-words";
import { NotOnBox, Skeleton } from "@/components/page";
import { PilotLight, type PilotState } from "@/components/pilot";
import { cn } from "@/lib/cn";
import { countWords, int, pct } from "@/lib/format";
import { relative } from "@/lib/time";
import { handlersOf, outsideOf, type Handler, type WorkflowLoad, retryUnlessDown } from "./model";
import { JobsArea, Label, type JobsSearch } from "./shared";

type Health = { state: PilotState; word: string; detail: string; down: boolean };

/** Whether an app is there to take its jobs, in a light and a few words. */
function health(rt: AppRuntime | undefined, loading: boolean): Health {
  if (!rt) return loading ? { state: "off", word: "Checking", detail: "", down: false } : { state: "off", word: "Unknown", detail: "Couldn’t read it", down: false };
  const p = rt.production;
  if (!p || (!p.live && !(p.instances ?? []).length)) return { state: "fault", word: "Not deployed", detail: rt.hint ?? "Deploy it to start taking jobs", down: true };
  if (p.stopped) return { state: "fault", word: "Stopped", detail: "The app was removed", down: true };
  if (p.sleeping) return { state: "off", word: "Asleep", detail: "Wakes on its next job", down: false };
  const all = p.instances ?? [];
  const up = all.filter((i) => i.running).length;
  if (up === 0) return { state: "fault", word: "Down", detail: all.length ? `${countWords(all.length, "instance")}, none running` : "No instances running", down: true };
  if (up < all.length) return { state: "on", word: "Partly up", detail: `${int(up)} of ${int(all.length)} instances running`, down: false };
  return { state: "on", word: "Running", detail: `${countWords(up, "instance")}`, down: false };
}

export function WorkersTab({ project, search }: { project: string; search: JobsSearch }) {
  useTitle(`${project} · Workers`);
  const stats = useQuery({ ...jq.stats(project), retry: retryUnlessDown });
  const crons = useQuery({ ...jq.crons(project), retry: retryUnlessDown });
  const runs = useQuery({ ...jq.runs(project), retry: retryUnlessDown });
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const apps = manifest.data?.manifest.apps;
  const handlers = handlersOf(apps, stats.data ?? [], crons.data ?? [], runs.data ?? []);
  const outside = outsideOf(stats.data ?? [], crons.data ?? []);
  const rts = useQueries({
    queries: handlers.map((h) => ({
      queryKey: ["runtime", project, h.app],
      queryFn: () => mod3.runtime(project, h.app),
      refetchInterval: 15_000,
      enabled: h.declared,
      retry: false,
    })),
  });
  if (stats.isError && notOnBox(stats.error)) return <NotOnBox what="Jobs" />;
  const healthOf = (i: number) => health(rts[i]?.data, !!rts[i]?.isPending && handlers[i].declared);
  const ready = stats.isSuccess && crons.isSuccess && manifest.isSuccess;

  // The sentence: anything waiting on an app that can't take it, else who does what.
  let said = "";
  if (ready && handlers.length) {
    const stuck = handlers
      .map((h, i) => ({ h, hl: healthOf(i), waiting: h.queues.reduce((n, x) => n + x.queued + x.retrying, 0) }))
      .filter((x) => x.hl.down && (x.waiting > 0 || x.h.crons.length > 0));
    if (stuck.length) {
      const s = stuck[0];
      said = `${s.h.app} is ${s.hl.word.toLowerCase()}, so ${s.waiting ? `${countWords(s.waiting, "job")} on ${s.h.queues.map((q) => q.name).join(" and ")} ${s.waiting === 1 ? "waits" : "wait"}` : `its ${countWords(s.h.crons.length, "schedule")} can’t run`}.`;
      if (stuck.length > 1) said += ` ${stuck.slice(1).map((x) => x.h.app).join(" and ")} ${stuck.length === 2 ? "is" : "are"} down too.`;
    } else {
      const hs = handlers.map((h, i) => ({ h, hl: healthOf(i) }));
      const n = handlers.length;
      said = `${countWords(n, "app", "apps", true)} ${n === 1 ? "processes" : "process"} jobs here${outside.length ? `, and ${countWords(outside.length, "address", "addresses")} outside the box ${outside.length === 1 ? "gets" : "get"} calls` : ""}.`;
      const asleep = hs.filter((x) => x.hl.word === "Asleep").map((x) => x.h.app);
      const idleDown = hs.filter((x) => x.hl.down).map((x) => x.h.app);
      if (hs.every((x) => x.hl.state === "on")) said += n === 1 ? " It’s running." : " All of them are running.";
      if (asleep.length) said += ` ${asleep.join(" and ")} ${asleep.length === 1 ? "is" : "are"} asleep until the next job wakes ${asleep.length === 1 ? "it" : "them"}.`;
      if (idleDown.length) said += ` ${idleDown.join(" and ")} ${idleDown.length === 1 ? "isn’t" : "aren’t"} running, but nothing waits on ${idleDown.length === 1 ? "it" : "them"}.`;
    }
  }

  return (
    <JobsArea project={project} tab="workers" search={search}>
      {said && <StateSentence className="mt-8">{said}</StateSentence>}
      {stats.isError && <JobsTrouble className="mt-8" error={stats.error} retry={() => void stats.refetch()} />}
      {!ready && !stats.isError && <Skeleton className="mt-8 h-56" />}
      {ready && handlers.length === 0 && outside.length === 0 && (
        <JobsStart className="mt-8" title="No app processes jobs yet." app={likelyApp(apps)} />
      )}
      {ready && handlers.length > 0 && (
        <section className="mt-8" aria-labelledby="apps-h">
          <h2 id="apps-h" className="sr-only">
            Apps that process jobs
          </h2>
          <div aria-hidden className="label hidden grid-cols-[minmax(0,15rem)_minmax(0,1fr)] gap-x-10 border-b border-rule-2 pb-2 md:grid">
            <span>App</span>
            <span>What it processes</span>
          </div>
          <ul className="divide-y divide-rule border-b border-rule max-md:border-t max-md:border-rule-2">
            {handlers.map((h, i) => (
              <HandlerRow key={h.app} project={project} h={h} hl={healthOf(i)} rt={rts[i]?.data} />
            ))}
          </ul>
        </section>
      )}
      {ready && outside.length > 0 && (
        <section className="mt-12" aria-labelledby="out-h">
          <Label id="out-h">Outside the box</Label>
          <p className="-mt-1 mb-3 text-[0.84375rem] text-ink-3">Queues and schedules can call any web address, signed. Tiffin can’t see those servers, only how their answers went.</p>
          <ul className="divide-y divide-rule border-y border-rule">
            {outside.map((o) => (
              <li key={o.host} className="grid gap-x-10 gap-y-2 py-3.5 md:grid-cols-[minmax(0,15rem)_minmax(0,1fr)]">
                <p className="ident truncate text-ink">{o.host}</p>
                <Work project={project} queues={o.queues} crons={o.crons} workflows={[]} />
              </li>
            ))}
          </ul>
        </section>
      )}
    </JobsArea>
  );
}

function HandlerRow({ project, h, hl, rt }: { project: string; h: Handler; hl: Health; rt?: AppRuntime }) {
  const last = rt?.production?.lastActive;
  const waiting = h.queues.reduce((n, x) => n + x.queued + x.retrying, 0);
  return (
    <li className="grid gap-x-10 gap-y-3 py-4 md:grid-cols-[minmax(0,15rem)_minmax(0,1fr)]">
      <div className="min-w-0">
        <p className="flex items-center gap-2">
          <PilotLight state={hl.state} label={hl.word} />
          {h.declared ? (
            <Link to="/projects/$project/apps/$app" params={{ project, app: h.app }} className="ident truncate text-ink hover:underline hover:underline-offset-4">
              {h.app}
            </Link>
          ) : (
            <span className="ident truncate text-ink">{h.app}</span>
          )}
          {h.app !== h.role && <span className="text-xs text-ink-3">{h.role === "worker" ? "worker" : "web app"}</span>}
        </p>
        <p className={cn("mt-1 text-[0.8125rem]", hl.down ? "text-danger" : "text-ink-2")}>
          {hl.word}
          {hl.detail && <span className={hl.down ? "text-ink-2" : "text-ink-3"}> · {hl.detail}</span>}
        </p>
        {!h.declared && <p className="mt-1 text-[0.8125rem] text-warn-ink">Not in tiffin.config.ts: jobs sent to it can’t be delivered.</p>}
        {last && <p className="mt-0.5 text-xs text-ink-3">Last active {relative(last)}</p>}
        {hl.down && waiting > 0 && <p className="mt-1 text-xs text-danger">{countWords(waiting, "job")} waiting for it</p>}
        {h.declared && (
          <Link to="/projects/$project/apps/$app/logs" params={{ project, app: h.app }} className="mt-1.5 inline-flex items-center gap-1 text-xs text-ink-3 hover:text-ink">
            Its logs <ArrowUpRight className="size-3" />
          </Link>
        )}
      </div>
      <Work project={project} queues={h.queues} crons={h.crons} workflows={h.workflows} idle={h.role === "worker"} />
    </li>
  );
}

/** An app's work: one line per queue, schedule and workflow, each with its load. */
function Work({ project, queues, crons, workflows, idle }: { project: string; queues: QueueStats[]; crons: QueueCron[]; workflows: WorkflowLoad[]; idle?: boolean }) {
  if (!queues.length && !crons.length && !workflows.length)
    return <p className="text-[0.84375rem] text-ink-3">{idle ? "Nothing yet. Point a queue or a schedule at it, or start a workflow it defines." : "Nothing."}</p>;
  return (
    <ul className="grid min-w-0 gap-1.5">
      {queues.map((x) => {
        const waiting = x.queued + x.retrying;
        return (
          <li key={`q-${x.name}`} className="grid grid-cols-[5.5rem_minmax(0,1fr)] items-baseline gap-x-3 text-[0.84375rem] sm:grid-cols-[5.5rem_minmax(0,14rem)_minmax(0,1fr)]">
            <span className="text-xs text-ink-3">queue</span>
            <Link to="/projects/$project/jobs" params={{ project }} search={{ queue: x.name }} className="ident truncate text-ink hover:underline hover:underline-offset-4" title={x.path ?? x.url}>
              {x.name}
              {x.paused && <span className="ml-2 font-sans text-xs font-[550] text-warn-ink">paused</span>}
            </Link>
            <span className="col-start-2 truncate text-[0.8125rem] text-ink-3 tnum sm:col-start-auto">
              {x.running > 0 && <span className="text-ink">{int(x.running)} running · </span>}
              {waiting > 0 && <span className={x.oldestQueuedSeconds > 300 ? "text-warn-ink" : "text-ink-2"}>{int(waiting)} waiting · </span>}
              {x.completedLastHour ? `${int(x.completedLastHour)} done this hour` : "quiet this hour"}
              {x.failedAttemptsLastHour > 0 && <span className={x.failureRate >= 0.2 ? "text-danger" : ""}> · {pct(x.failureRate)} failing</span>}
              {x.dead > 0 && <span className="text-danger"> · {int(x.dead)} gave up</span>}
              <span className="max-lg:hidden"> · {x.concurrency > 0 ? `${int(x.concurrency)} at once` : "no limit"}</span>
            </span>
          </li>
        );
      })}
      {crons.map((c) => {
        const h = cronHuman(c.schedule);
        return (
          <li key={`c-${c.name}`} className="grid grid-cols-[5.5rem_minmax(0,1fr)] items-baseline gap-x-3 text-[0.84375rem] sm:grid-cols-[5.5rem_minmax(0,14rem)_minmax(0,1fr)]">
            <span className="text-xs text-ink-3">schedule</span>
            <Link to="/projects/$project/jobs/schedules" params={{ project }} className="ident truncate text-ink hover:underline hover:underline-offset-4">
              {c.name}
              {c.paused && <span className="ml-2 font-sans text-xs font-[550] text-warn-ink">paused</span>}
            </Link>
            <span className="col-start-2 truncate text-[0.8125rem] text-ink-3 sm:col-start-auto">
              {h.exact ? h.words : c.schedule}
              {!c.paused && <> · next {relative(c.nextAt)}</>}
              {c.lastState === "dead" && <span className="text-danger"> · last run failed</span>}
            </span>
          </li>
        );
      })}
      {workflows.map((w) => (
        <li key={`w-${w.name}`} className="grid grid-cols-[5.5rem_minmax(0,1fr)] items-baseline gap-x-3 text-[0.84375rem] sm:grid-cols-[5.5rem_minmax(0,14rem)_minmax(0,1fr)]">
          <span className="text-xs text-ink-3">workflow</span>
          <Link to="/projects/$project/jobs" params={{ project }} search={{ kind: "workflow", q: w.name }} className="ident truncate text-ink hover:underline hover:underline-offset-4">
            {w.name}
          </Link>
          <span className="col-start-2 truncate text-[0.8125rem] text-ink-3 tnum sm:col-start-auto">
            {w.active > 0 && <span className="text-ink">{int(w.active)} going · </span>}
            {countWords(w.runs, "recent run")}, last {relative(w.lastAt)}
            {w.failed > 0 && <span className="text-danger"> · {int(w.failed)} failed</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}
