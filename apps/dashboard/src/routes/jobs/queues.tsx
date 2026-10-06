// Queues: each queue with its depth, throughput, failures, last run and its
// limit on jobs at once; a switch pauses it. Topics below, with their
// subscribers added and removed in place.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { MoreHorizontal, X } from "lucide-react";
import { useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { jobsApi, jq } from "@/api/jobs";
import { mod2, type QueueStats, type QueueTopic } from "@/api/modules";
import { q as api } from "@/api/queries";
import { Breaker } from "@/components/breaker";
import { useTitle } from "@/components/favicon";
import { EmptyJobs } from "@/components/jobs-words";
import { NotOnBox, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Throttle } from "@/components/throttle";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { int, ms, pct } from "@/lib/format";
import { useMe } from "@/lib/me";
import { change, editKey, usePending } from "@/lib/staged";
import { relative } from "@/lib/time";
import { JobsArea, Label, TargetWords, type JobsSearch } from "./shared";

/** Concurrency detents. 0 means no limit; the throttle shows it as the last stop. */
const NO_LIMIT = 1000;
const STOPS = [1, 2, 4, 8, 16, 32, NO_LIMIT];
const toStop = (c: number) => (c <= 0 ? NO_LIMIT : c);
const fromStop = (s: number) => (s >= NO_LIMIT ? 0 : s);
const letRun = (c: number) => (c <= 0 ? "any number of jobs" : `${int(c)} ${c === 1 ? "job" : "jobs"}`);

const cols = "md:grid-cols-[minmax(0,1fr)_4.5rem_4.5rem_5.5rem_6rem_9.5rem_7.5rem]";

export function QueuesTab({ project, search }: { project: string; search: JobsSearch }) {
  useTitle(`${project} · Queues`);
  const navigate = useNavigate();
  const { can } = useMe();
  const stats = useQuery(jq.stats(project));
  const topics = useQuery(jq.topics(project));
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const edits = usePending(project);
  if (stats.isError && notOnBox(stats.error)) return <NotOnBox what="Jobs" />;
  const all = stats.data ?? [];
  const visible = all.filter((x) => !x.name.startsWith("_") && !x.topic);
  const system = all.filter((x) => x.name.startsWith("_"));
  const declared = manifest.data?.manifest.queues ?? {};
  const open = (d: "queue" | "send", name?: string) => void navigate({ to: ".", search: (s: JobsSearch) => ({ ...s, do: d, name }) } as never);

  const stageConcurrency = (x: QueueStats, stop: number) => {
    const c = fromStop(stop);
    if (declared[x.name]) {
      change(project, { kind: "set", path: ["queues", x.name, "concurrency"], from: x.concurrency, to: c, what: `Let ${x.name} run ${letRun(c)} at once`, undo: `${x.name} goes back to ${letRun(x.concurrency)} at once` });
      return;
    }
    if (c === x.concurrency) return;
    const target = x.url ? { url: x.url } : { app: x.app ?? "", path: x.path ?? `/queues/${x.name}` };
    change(project, {
      kind: "set",
      path: ["queues", x.name],
      from: undefined,
      to: { ...target, concurrency: c, keyConcurrency: x.keyConcurrency, rateLimit: x.rateLimit, ratePeriodSeconds: x.ratePeriodSeconds, maxAttempts: x.maxAttempts, leaseSeconds: x.leaseSeconds },
      what: `Declare ${x.name} in tiffin.config.ts and let it run ${letRun(c)} at once`,
      undo: `${x.name} leaves tiffin.config.ts again, which empties its waiting jobs, so the undo asks you first`,
    });
  };
  const staged = (name: string): number | undefined => {
    for (const e of edits) {
      if (e.kind !== "set") continue;
      const k = editKey(e);
      if (k === `set:queues/${name}/concurrency`) return e.to as number;
      if (k === `set:queues/${name}`) return (e.to as { concurrency?: number } | undefined)?.concurrency;
    }
    return undefined;
  };

  return (
    <JobsArea
      project={project}
      tab="queues"
      search={search}
      actions={
        can("apply:reversible") ? (
          <>
            <Button onClick={() => open("send")} title="Send a test job (T)" aria-keyshortcuts="t">
              Send a test job
            </Button>
            <Button variant="primary" onClick={() => open("queue")} title="New queue (U)" aria-keyshortcuts="u">
              New queue
            </Button>
          </>
        ) : undefined
      }
    >
      {stats.isError && <ProblemNote className="mt-6" error={stats.error} />}
      <section className="mt-8" aria-labelledby="queues-h">
        <h2 id="queues-h" className="label mb-2.5 md:sr-only">
          Queues
        </h2>
        <div aria-hidden className={cn("label hidden gap-x-4 pb-2", cols, visible.length > 0 && "md:grid")}>
          <span>Queue</span>
          <span className="text-right">Waiting</span>
          <span className="text-right">Running</span>
          <span className="text-right">Done, hour</span>
          <span className="text-right">Failing</span>
          <span className="pl-1">At once</span>
          <span className="pr-9 text-right">On</span>
        </div>
        <ul className="divide-y divide-rule border-y border-rule-2">
          {stats.isPending && (
            <li className="py-3">
              <Skeleton className="h-24" />
            </li>
          )}
          {visible.map((x) => (
            <QueueRow
              key={x.name}
              project={project}
              x={x}
              staged={staged(x.name)}
              declared={!!declared[x.name]}
              canStage={can("apply:reversible") && manifest.isSuccess}
              canAct={can("apply:reversible")}
              onStage={(s) => stageConcurrency(x, s)}
              onSettings={() => open("queue", x.name)}
              onSend={() => open("send", x.name)}
            />
          ))}
          {stats.isSuccess && visible.length === 0 && (
            <li>
              <EmptyJobs title="No queues yet.">
                A queue delivers jobs one by one to an app route or a web address, with retries. Make one with New queue, or send from an app:{" "}
                <code className="ident text-ink">queue.send("emails", …)</code>.
              </EmptyJobs>
            </li>
          )}
        </ul>
        {system.length > 0 && <SystemLine project={project} system={system} />}
      </section>
      <Topics project={project} list={topics.data} stats={visible} declared={manifest.data?.manifest.topics ?? {}} />
    </JobsArea>
  );
}

function Count({ n, tone, className }: { n: number; tone?: "warn" | "danger"; className?: string }) {
  return <span className={cn("text-right text-[0.875rem] tnum", n === 0 ? "text-ink-4" : tone === "danger" ? "text-danger" : tone === "warn" ? "text-warn-ink" : "text-ink", className)}>{int(n)}</span>;
}

function QueueRow({
  project,
  x,
  staged,
  declared,
  canStage,
  canAct,
  onStage,
  onSettings,
  onSend,
}: {
  project: string;
  x: QueueStats;
  staged?: number;
  declared: boolean;
  canStage: boolean;
  canAct: boolean;
  onStage: (stop: number) => void;
  onSettings: () => void;
  onSend: () => void;
}) {
  const qc = useQueryClient();
  const [preview, setPreview] = useState<number | null>(null);
  const waiting = x.queued + x.retrying;
  const shown = preview !== null ? fromStop(preview) : (staged ?? x.concurrency);
  const failing = x.failedAttemptsLastHour > 0;
  const refresh = () => void qc.invalidateQueries({ queryKey: ["queue-stats", project] });
  const toggle = useMutation({
    mutationFn: (on: boolean) => (on ? mod2.resume(project, x.name) : mod2.pause(project, x.name)),
    onSuccess: (_, on) => {
      refresh();
      toast({
        title: on ? `Resumed ${x.name}.` : `Paused ${x.name}. New jobs wait until you resume it.`,
        detail: on ? undefined : "Jobs already running finish where they are.",
        action: { label: "Undo", run: async () => (on ? mod2.pause(project, x.name) : mod2.resume(project, x.name)).then(refresh) },
      });
    },
    onError: (e) => toast({ title: "That didn’t work.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  const deliverable = !!(x.app || x.url);
  const lever = !deliverable ? (
    <span className="text-sm text-ink-4">–</span>
  ) : canStage ? (
    <Throttle
      size="mini"
      label={`${x.name}: jobs at once`}
      unit="at once"
      stops={STOPS}
      value={toStop(staged ?? x.concurrency)}
      applied={toStop(x.concurrency)}
      format={(n) => (n >= NO_LIMIT ? "no limit" : int(n))}
      onChange={setPreview}
      onCommit={(n) => {
        setPreview(null);
        onStage(n);
      }}
    />
  ) : (
    <span className="text-[0.8125rem] whitespace-nowrap text-ink-2 tnum" title={declared ? "Set in tiffin.config.ts" : "Not in tiffin.config.ts yet"}>
      {shown <= 0 ? "no limit" : `${int(shown)} at once`}
    </span>
  );
  const actions = canAct ? (
    <span className="flex items-center justify-end gap-1">
      <Breaker label={`${x.name} delivers jobs`} state={x.paused ? "off" : "on"} staged={toggle.isPending ? (toggle.variables ? "on" : "off") : undefined} onFlip={(n) => toggle.mutate(n === "on")} />
      <Menu>
        <MenuTrigger asChild>
          <Button size="icon-sm" variant="ghost" aria-label={`More for ${x.name}`}>
            <MoreHorizontal />
          </Button>
        </MenuTrigger>
        <MenuContent align="end">
          <MenuItem onSelect={onSend} disabled={!deliverable}>
            Send a test job…
          </MenuItem>
          <MenuItem onSelect={onSettings}>Settings…</MenuItem>
          <MenuItem asChild>
            <Link to="/projects/$project/jobs" params={{ project }} search={{ queue: x.name }}>
              Its runs
            </Link>
          </MenuItem>
        </MenuContent>
      </Menu>
    </span>
  ) : null;
  const where = (
    <span className="truncate">
      <TargetWords app={x.app} path={x.path} url={x.url} />
      {x.lastRunAt && <span> · last ran {relative(x.lastRunAt)}</span>}
      {x.p95Ms > 0 && <span> · p95 {ms(x.p95Ms)}</span>}
    </span>
  );
  return (
    <li>
      <div className={cn("hidden items-center gap-x-4 py-3 md:grid", cols)}>
        <div className="min-w-0">
          <Link to="/projects/$project/jobs" params={{ project }} search={{ queue: x.name }} className="group inline-flex max-w-full items-baseline gap-2" aria-label={`${x.name}: its runs`}>
            <span className="ident truncate text-ink group-hover:underline group-hover:underline-offset-4">{x.name}</span>
            {x.paused && <span className="text-[0.8125rem] font-[550] text-warn-ink">Paused</span>}
          </Link>
          <p className="mt-0.5 flex text-xs text-ink-3">{where}</p>
        </div>
        <span className="text-right">
          <Count n={waiting} tone={x.oldestQueuedSeconds > 300 ? "warn" : undefined} />
          {x.oldestQueuedSeconds > 300 && <span className="block text-xs text-warn-ink">oldest {ms(x.oldestQueuedSeconds * 1000)}</span>}
        </span>
        <Count n={x.running} />
        <Count n={x.completedLastHour} />
        <span className="text-right">
          {failing ? <span className={cn("text-[0.875rem] tnum", x.failureRate >= 0.2 ? "text-danger" : "text-ink-2")}>{pct(x.failureRate)}</span> : <span className="text-[0.875rem] text-ink-4">–</span>}
          {x.dead > 0 && <span className="block text-xs text-danger">{int(x.dead)} gave up</span>}
        </span>
        <div className="pl-1">{lever}</div>
        {actions ?? <span />}
      </div>

      {/* phone: the same row, stacked; nothing scrolls sideways */}
      <div className="py-3 md:hidden">
        <div className="flex items-center gap-3">
          <Link to="/projects/$project/jobs" params={{ project }} search={{ queue: x.name }} className="min-w-0 flex-1">
            <span className="flex items-baseline gap-2">
              <span className="ident truncate text-ink">{x.name}</span>
              {x.paused && <span className="text-[0.8125rem] font-[550] text-warn-ink">Paused</span>}
            </span>
            <span className="mt-0.5 flex text-xs text-ink-3">{where}</span>
          </Link>
          {actions}
        </div>
        <p className="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[0.8125rem] text-ink-2 tnum">
          <span className={x.completedLastHour ? "" : "text-ink-3"}>{x.completedLastHour ? `${int(x.completedLastHour)} done this hour` : "Quiet this hour"}</span>
          {waiting > 0 && <span className={x.oldestQueuedSeconds > 300 ? "text-warn-ink" : ""}>{int(waiting)} waiting</span>}
          {x.running > 0 && <span>{int(x.running)} running</span>}
          {failing && <span className={x.failureRate >= 0.2 ? "text-danger" : ""}>{pct(x.failureRate)} of tries failing</span>}
          {x.dead > 0 && <span className="text-danger">{int(x.dead)} gave up</span>}
        </p>
        {deliverable && <div className="mt-2">{lever}</div>}
      </div>
    </li>
  );
}

function SystemLine({ project, system }: { project: string; system: QueueStats[] }) {
  const done = system.reduce((n, x) => n + x.completedLastHour, 0);
  const dead = system.reduce((n, x) => n + x.dead, 0);
  return (
    <p className="mt-3 text-[0.8125rem] text-ink-3">
      Behind the scenes, schedules and workflow turns run on{" "}
      {system.map((x, i) => (
        <span key={x.name}>
          {i > 0 && " and "}
          <Link to="/projects/$project/jobs" params={{ project }} search={{ queue: x.name }} className="ident text-[0.75rem] text-ink-2 hover:text-ink">
            {x.name}
          </Link>
        </span>
      ))}
      : {int(done)} done this hour{dead ? <span className="text-danger">, {int(dead)} gave up</span> : null}.
    </p>
  );
}

// ------------------------------------------------------------------ topics

/**
 * A topic sends each message to every queue that subscribes. A topic declared
 * in tiffin.config.ts changes through the config (History, Undo); one an app
 * made at runtime changes through the API.
 */
function Topics({ project, list, stats, declared }: { project: string; list?: QueueTopic[]; stats: QueueStats[]; declared: Record<string, { subscribers?: string[] | null }> }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [adding, setAdding] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [first, setFirst] = useState("");
  const queues = stats.filter((x) => x.app || x.url).map((x) => x.name);
  const declaredQueues = Object.keys(useQuery({ ...api.manifest(project), retry: false }).data?.manifest.queues ?? {});
  const refresh = () => void qc.invalidateQueries({ queryKey: ["topics", project] });
  const viaApi = useMutation({
    mutationFn: async ({ topic, queue, add }: { topic: string; queue: string; add: boolean }) => {
      if (!add) return jobsApi.unsubscribe(project, topic, queue);
      const s = stats.find((x) => x.name === queue);
      return jobsApi.subscribe(project, topic, queue, s?.url ? { url: s.url } : { app: s?.app, path: s?.path });
    },
    onSuccess: refresh,
    onError: (e) => toast({ title: "That didn’t work.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  const setSubs = (topic: string, subs: string[], what: string, undo: string) =>
    change(project, { kind: "set", path: ["topics", topic], from: declared[topic], to: { subscribers: subs }, what, undo }, { immediate: true });
  const add = (t: QueueTopic, queue: string) => {
    if (declared[t.name]) {
      if (!declaredQueues.includes(queue)) {
        toast({ title: `${queue} isn’t in tiffin.config.ts yet.`, detail: "Open its Settings and save, then subscribe it.", tone: "danger" });
        return;
      }
      const cur = declared[t.name].subscribers ?? [];
      setSubs(t.name, [...cur, queue].sort(), `Subscribe ${queue} to ${t.name}`, `${queue} stops getting ${t.name} messages`);
    } else viaApi.mutate({ topic: t.name, queue, add: true });
    setAdding(null);
  };
  const remove = (t: QueueTopic, queue: string) => {
    if (declared[t.name]) setSubs(t.name, (declared[t.name].subscribers ?? []).filter((s) => s !== queue), `Remove ${queue} from ${t.name}`, `${queue} gets ${t.name} messages again`);
    else viaApi.mutate({ topic: t.name, queue, add: false });
  };
  const create = () => {
    if (!/^[a-z][a-z0-9.-]{0,63}$/.test(newName) || !first) return;
    change(project, { kind: "set", path: ["topics", newName], from: undefined, to: { subscribers: [first] }, what: `Add the ${newName} topic, sent to ${first}`, undo: `the ${newName} topic is removed` }, { immediate: true });
    setNewName("");
    setFirst("");
  };
  const topics = list ?? [];
  return (
    <section className="mt-14 max-w-[48rem]" aria-labelledby="topics-h">
      <Label id="topics-h">Topics</Label>
      <p className="mb-3 text-[0.84375rem] text-ink-3">A topic sends each message to every queue that subscribes, and each queue retries it on its own.</p>
      {topics.length > 0 && (
        <ul className="divide-y divide-rule border-y border-rule">
          {topics.map((t) => {
            const subs = (t.subscriptions ?? []).map((s) => s.name);
            const free = queues.filter((q) => !subs.includes(q));
            return (
              <li key={t.name} className="py-3">
                <p className="flex items-baseline gap-2">
                  <span className="ident text-ink">{t.name}</span>
                  {!declared[t.name] && <span className="text-xs text-ink-3">made by an app</span>}
                </p>
                <ul className="mt-1.5 flex flex-wrap items-center gap-1.5" aria-label={`${t.name} subscribers`}>
                  {subs.map((s) => (
                    <li key={s} className="inline-flex h-7 items-center gap-1 rounded-full border border-rule-2 pr-1 pl-2.5 text-[0.8125rem] text-ink-2">
                      <span className="ident text-[0.75rem]">{s}</span>
                      {can("apply:reversible") && (
                        <button type="button" onClick={() => remove(t, s)} aria-label={`Remove ${s} from ${t.name}`} className="grid size-5 place-items-center rounded-full text-ink-3 hover:bg-paper-hover hover:text-ink">
                          <X className="size-3" />
                        </button>
                      )}
                    </li>
                  ))}
                  {subs.length === 0 && <li className="text-[0.8125rem] text-ink-3">No subscribers: messages go nowhere.</li>}
                  {can("apply:reversible") && free.length > 0 && (
                    <li>
                      {adding === t.name ? (
                        <select
                          autoFocus
                          aria-label={`Subscribe a queue to ${t.name}`}
                          defaultValue=""
                          onChange={(e) => e.target.value && add(t, e.target.value)}
                          onBlur={() => setAdding(null)}
                          className="h-7 rounded-full border border-rule-2 bg-paper px-2 font-mono text-[0.75rem] text-ink"
                        >
                          <option value="" disabled>
                            Pick a queue
                          </option>
                          {free.map((q) => (
                            <option key={q} value={q}>
                              {q}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <button type="button" onClick={() => setAdding(t.name)} className="h-7 rounded-full border border-dashed border-rule-3 px-2.5 text-[0.8125rem] text-ink-3 hover:text-ink">
                          Add a queue
                        </button>
                      )}
                    </li>
                  )}
                </ul>
              </li>
            );
          })}
        </ul>
      )}
      {can("apply:reversible") && declaredQueues.length > 0 && (
        <form
          className="mt-3 flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            create();
          }}
        >
          <input
            value={newName}
            onChange={(e) => setNewName(e.target.value.toLowerCase().replace(/[^a-z0-9.-]/g, "-"))}
            placeholder="order.created"
            aria-label="New topic name"
            spellCheck={false}
            className="h-8 w-44 rounded-[7px] border border-rule-2 bg-paper px-2.5 font-mono text-[0.78rem] text-ink outline-none focus-visible:border-brass"
          />
          <select value={first} onChange={(e) => setFirst(e.target.value)} aria-label="First subscriber" className="h-8 rounded-[7px] border border-rule-2 bg-paper px-2 font-mono text-[0.78rem] text-ink">
            <option value="">sent to…</option>
            {declaredQueues.map((q) => (
              <option key={q} value={q}>
                {q}
              </option>
            ))}
          </select>
          <Button type="submit" size="md" disabled={!/^[a-z][a-z0-9.-]{0,63}$/.test(newName) || !first || !!declared[newName]}>
            Add topic
          </Button>
        </form>
      )}
      {topics.length === 0 && declaredQueues.length === 0 && <p className="text-[0.84375rem] text-ink-3">No topics yet. Make a queue first: a topic sends to queues.</p>}
    </section>
  );
}
