// Schedules: each cron in plain words and as written, what it calls, how
// its last ten runs went, and its next three runs (in its own time zone,
// with the reader's clock when it differs). A switch pauses it; Run now runs
// it once; Edit opens the form.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { notOnBox, ApiError } from "@/api/client";
import { jobsApi, jq, type CronRun, type QueueCron } from "@/api/jobs";
import { Breaker } from "@/components/breaker";
import { useTitle } from "@/components/favicon";
import { cronHuman, StateSentence } from "@/components/jobs-words";
import { JobsStart, JobsTrouble, likelyApp } from "@/components/jobs-start";
import { q as api } from "@/api/queries";
import { NotOnBox, Skeleton } from "@/components/page";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { ms, pct, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";
import { retryUnlessDown } from "./model";
import { JobsArea, TargetWords, whoWords, type JobsSearch } from "./shared";

const nextFmt = (tz: string) => {
  try {
    return new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: tz });
  } catch {
    return new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC" });
  }
};

/** "Every weekday at 09:00", in the schedule's own time zone. */
export function whenWords(c: Pick<QueueCron, "schedule" | "timezone">) {
  const h = cronHuman(c.schedule);
  const w = h.exact ? h.words.charAt(0).toUpperCase() + h.words.slice(1) : `On the schedule ${c.schedule}`;
  return { words: w, zone: h.timeOfDay || !h.exact ? c.timezone : "" };
}

const dot: Record<string, string> = {
  completed: "bg-ink-3",
  dead: "bg-danger",
  running: "bg-brass",
  retrying: "bg-warn",
  queued: "border border-ink-3 bg-transparent",
  scheduled: "border border-ink-3 bg-transparent",
  cancelled: "bg-rule-2",
};
const runWord: Record<string, string> = { completed: "done", dead: "failed", running: "running", retrying: "retrying", queued: "waiting", scheduled: "waiting", cancelled: "cancelled" };

/** The last ten runs as small marks, oldest first; each is a link. */
function Recent({ project, runs }: { project: string; runs: CronRun[] }) {
  const last = runs.slice(0, 10).reverse();
  return (
    <span className="inline-flex items-center gap-[3px]" role="list" aria-label={last.length ? `Its last ${last.length} runs, oldest first` : "No runs yet"}>
      {Array.from({ length: 10 - last.length }, (_, i) => (
        <i key={`e${i}`} aria-hidden className="mx-[5px] block h-3 w-1.5 rounded-[2px] bg-rule" />
      ))}
      {last.map((r) => (
        <Link
          key={r.job}
          role="listitem"
          to="/projects/$project/jobs/$id"
          params={{ project, id: r.job }}
          className="grid size-4 place-items-center rounded-[3px] hover:bg-paper-hover"
          title={`${runWord[r.state] ?? r.state}, ${relative(r.at)}${r.durationMs !== undefined ? `, ${ms(r.durationMs)}` : ""}`}
          aria-label={`${r.job}: ${runWord[r.state] ?? r.state} ${relative(r.at)}`}
        >
          <i aria-hidden className={cn("block h-3 w-1.5 rounded-[2px]", dot[r.state] ?? "bg-rule-2")} />
        </Link>
      ))}
    </span>
  );
}

function health(c: QueueCron): { text: string; bad: boolean } {
  const finished = (c.recent ?? []).filter((r) => r.state === "completed" || r.state === "dead");
  const failed = finished.filter((r) => r.state === "dead").length;
  if (finished.length === 0) return { text: c.lastAt ? `Last ran ${relative(c.lastAt)}` : "Hasn’t run yet", bad: false };
  if (failed === 0) return { text: finished.length === 1 ? "Ran once; it worked" : `The last ${words(finished.length)} runs worked`, bad: false };
  return { text: `${words(failed, true)} of the last ${words(finished.length)} runs failed (${pct(c.failureRate)})`, bad: true };
}

export function SchedulesTab({ project, search }: { project: string; search: JobsSearch }) {
  useTitle(`${project} · Schedules`);
  const navigate = useNavigate();
  const { can } = useMe();
  const crons = useQuery({ ...jq.crons(project), retry: retryUnlessDown });
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  if (crons.isError && notOnBox(crons.error)) return <NotOnBox what="Jobs" />;
  const list = crons.data ?? [];
  const paused = list.filter((c) => c.paused);
  const next = list.filter((c) => !c.paused).sort((a, b) => a.nextAt.localeCompare(b.nextAt))[0];
  return (
    <JobsArea project={project} tab="schedules" search={search}>
      {list.length > 0 && (
        <StateSentence className="mt-8">
          {next ? `Next up: ${next.name}, ${relative(next.nextAt)}.` : "Every schedule is paused."}
          {paused.length > 0 && next ? ` ${paused.map((c) => c.name).join(" and ")} ${paused.length === 1 ? "is" : "are"} paused.` : ""}
        </StateSentence>
      )}
      {crons.isError && <JobsTrouble className="mt-8" error={crons.error} retry={() => void crons.refetch()} />}
      <section className="mt-8" aria-label="Schedules">
        {crons.isPending ? (
          <Skeleton className="h-32" />
        ) : crons.isError ? null : list.length === 0 ? (
          <JobsStart
            title="No schedules yet."
            focus="schedule"
            app={likelyApp(manifest.data?.manifest.apps)}
            actions={
              can("apply:reversible") ? (
                <Button size="md" onClick={() => void navigate({ to: ".", search: (s: JobsSearch) => ({ ...s, do: "schedule" }) } as never)}>
                  New schedule
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ul className="divide-y divide-rule border-y border-rule-2">
            {list.map((c) => (
              <CronRow key={c.name} project={project} c={c} />
            ))}
          </ul>
        )}
      </section>
    </JobsArea>
  );
}

function CronRow({ project, c }: { project: string; c: QueueCron }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useMe();
  const edit = can("apply:reversible");
  const w = whenWords(c);
  const h = health(c);
  const refresh = () => void qc.invalidateQueries({ queryKey: ["crons", project] });
  const pause = useMutation({
    mutationFn: (on: boolean) => (on ? jobsApi.resumeCron(project, c.name) : jobsApi.pauseCron(project, c.name)),
    onSuccess: (_, on) => {
      refresh();
      toast({
        title: on ? `${c.name} runs on schedule again.` : `Paused ${c.name}.`,
        detail: on ? "Runs it missed while paused are skipped." : "It won’t run until you switch it back on. Run now still works.",
        action: { label: "Undo", run: async () => (on ? jobsApi.pauseCron(project, c.name) : jobsApi.resumeCron(project, c.name)).then(refresh) },
      });
    },
    onError: (e) => toast({ title: "That didn’t work.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  const run = useMutation({
    mutationFn: () => jobsApi.triggerCron(project, c.name),
    onSuccess: (r) => {
      refresh();
      void qc.invalidateQueries({ queryKey: ["jobs", project] });
      toast({ title: `Started ${c.name} now.`, detail: "Its regular runs stay as they are.", action: { label: "Watch it", run: () => void navigate({ to: "/projects/$project/jobs/$id", params: { project, id: r.job } }) } });
    },
    onError: (e) => toast({ title: "Couldn’t start it.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  const shown = pause.isPending ? !pause.variables : !c.paused;
  const openEdit = () => void navigate({ to: ".", search: (s: JobsSearch) => ({ ...s, do: "schedule", name: c.name }) } as never);
  return (
    <li className="grid gap-x-6 gap-y-2 py-3.5 md:grid-cols-[minmax(0,1fr)_minmax(0,15rem)_auto] md:items-center">
      <div className="min-w-0">
        <p className="flex flex-wrap items-baseline gap-x-2">
          <span className="ident text-ink">{c.name}</span>
          {c.paused && <span className="text-[0.8125rem] font-[550] text-warn-ink">Paused</span>}
          {c.origin === "vercel.json" && <span className="text-xs text-ink-3">from {c.app}’s vercel.json</span>}
        </p>
        <p className="mt-0.5 flex flex-wrap items-baseline gap-x-2 text-[0.875rem] text-ink-2">
          <span>{w.words}</span>
          <code className="rounded-[4px] bg-paper-sunk px-1.5 font-mono text-[0.72rem] text-ink-3" title="As written in the config">
            {c.schedule}
          </code>
          {w.zone && <span className="text-[0.8125rem] text-ink-3">{w.zone}</span>}
        </p>
        <p className="mt-0.5 truncate text-xs text-ink-3">
          <TargetWords app={c.app} path={c.path} url={c.url} />
          {c.paused && c.pausedBy ? ` · paused by ${whoWords(c.pausedBy)} ${c.pausedAt ? relative(c.pausedAt) : ""}` : ""}
        </p>
      </div>
      <div className="grid gap-1 text-[0.8125rem]">
        <Recent project={project} runs={c.recent ?? []} />
        <span className={cn(h.bad ? "text-danger" : "text-ink-3")}>{h.text}</span>
        <NextRuns project={project} c={c} />
      </div>
      {edit && (
        <div className="flex items-center gap-1.5 md:justify-end">
          <span className="mr-1 inline-flex items-center gap-2">
            <Breaker label={`${c.name} runs on schedule`} state={c.paused ? "off" : "on"} staged={pause.isPending ? (shown ? "on" : "off") : undefined} onFlip={(n) => pause.mutate(n === "on")} />
            <span className="w-12 text-[0.8125rem] text-ink-2">{shown ? "On" : "Paused"}</span>
          </span>
          <Button size="sm" variant="ghost" onClick={() => run.mutate()} disabled={run.isPending}>
            Run now
          </Button>
          <Button size="sm" variant="ghost" onClick={openEdit}>
            Edit
          </Button>
        </div>
      )}
    </li>
  );
}

/** The next three runs: the first with how long until it, the others as times. */
function NextRuns({ project, c }: { project: string; c: QueueCron }) {
  const preview = useQuery({
    queryKey: ["schedule-preview", project, c.schedule, c.timezone],
    queryFn: () => jobsApi.preview(project, c.schedule, c.timezone),
    enabled: !c.paused,
    staleTime: 300_000,
    retry: false,
  });
  if (c.paused) return <span className="text-ink-3">Won’t run while paused</span>;
  const fmt = nextFmt(c.timezone);
  const next = (preview.data?.next ?? []).filter((t) => new Date(t).getTime() >= new Date(c.nextAt).getTime() - 1000);
  const first = next[0] ?? c.nextAt;
  const later = next.slice(1, 3);
  return (
    <span className="text-ink-3 tnum">
      Next <span className="text-ink-2">{fmt.format(new Date(first))}</span> ({relative(first)})
      {later.length > 0 && <span className="block text-xs">then {later.map((t) => fmt.format(new Date(t))).join(", ")}</span>}
    </span>
  );
}
