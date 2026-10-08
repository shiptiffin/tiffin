// The Jobs area's frame: one header with Runs · Schedules · Queues · Workers · Failed,
// the actions every tab shares (send a test job, a new schedule or queue, a
// workflow run) with their keys, and the dialogs those open. Dialogs are in
// the URL (?do=schedule), so the command palette can open them too.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { lazy, Suspense, useEffect, type ReactNode } from "react";
import { jq } from "@/api/jobs";
import { q as api, queryClient } from "@/api/queries";
import { ConnectButton } from "@/components/connect";
import { Crumbs, Page, PageHeader, Untrusted } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/dropdown";
import { actorWords } from "@/lib/actors";
import { cn } from "@/lib/cn";
import { int, words } from "@/lib/format";
import type { JobsDo, JobsSearch } from "@/lib/jobs-search";
import { useMe } from "@/lib/me";
import { useCommand } from "@/lib/shortcuts";
import { handlersOf, ownQueues, retryUnlessDown } from "./model";

export type JobsTab = "runs" | "schedules" | "queues" | "workers" | "failed";
export type { JobsDo, JobsSearch };

// Workers has no route of its own yet: /jobs/workers lands on the $id route, which opens it (see detail.tsx).
const tabs: Array<{ tab: JobsTab; label: string; to: string; params?: Record<string, string> }> = [
  { tab: "runs", label: "Runs", to: "/projects/$project/jobs" },
  { tab: "schedules", label: "Schedules", to: "/projects/$project/jobs/schedules" },
  { tab: "queues", label: "Queues", to: "/projects/$project/jobs/queues" },
  { tab: "workers", label: "Workers", to: "/projects/$project/jobs/$id", params: { id: "workers" } },
  { tab: "failed", label: "Failed", to: "/projects/$project/jobs/failed" },
];

const Forms = {
  schedule: lazy(() => import("./forms").then((m) => ({ default: m.ScheduleForm }))),
  queue: lazy(() => import("./forms").then((m) => ({ default: m.QueueForm }))),
  send: lazy(() => import("./forms").then((m) => ({ default: m.SendJobForm }))),
  run: lazy(() => import("./forms").then((m) => ({ default: m.StartRunForm }))),
};

export function JobsArea({ project, tab, search, children, actions }: { project: string; tab: JobsTab; search: JobsSearch; children: ReactNode; actions?: ReactNode }) {
  const navigate = useNavigate();
  const { can } = useMe();
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const apps = Object.keys(manifest.data?.manifest.apps ?? {});
  const editable = can("apply:reversible");
  // A change to tiffin.config.ts (a schedule or queue added, edited, removed) shows at once.
  const qc = useQueryClient();
  const changed = manifest.dataUpdatedAt;
  useEffect(() => {
    const again = () => ["crons", "queue-stats", "topics"].forEach((k) => void qc.invalidateQueries({ queryKey: [k, project] }));
    again();
    const t = setTimeout(again, 1500); // the box applies it to the queue a moment later
    return () => clearTimeout(t);
  }, [changed, project, qc]);
  const open = (d: JobsDo | undefined, name?: string) =>
    void navigate({ to: ".", search: (s: JobsSearch) => ({ ...s, do: d, name: d ? name : undefined }), replace: !d });
  // The area's actions: keys, ⌘K ("On this page"), and requests from elsewhere (the "Just a schedule" starter asks for new-schedule).
  const go = (d: JobsDo) => void navigate({ to: ".", search: (x: JobsSearch) => ({ ...x, do: d, name: undefined }) });
  useCommand(editable ? { id: "new-schedule", label: "New schedule", keys: "n", keywords: ["cron", "every", "timer"], run: () => go("schedule") } : null);
  useCommand(editable ? { id: "new-queue", label: "New queue", keys: "u", keywords: ["create", "queue"], run: () => go("queue") } : null);
  useCommand(editable ? { id: "send-test-job", label: "Send a test job", keys: "t", keywords: ["queue", "payload", "try"], run: () => go("send") } : null);
  useCommand(editable && apps.length > 0 ? { id: "start-run", label: "Start a workflow run", keys: "w", keywords: ["workflow", "run"], run: () => go("run") } : null);
  const Form = search.do ? Forms[search.do] : null;
  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Jobs" }]} />}
        title="Jobs"
        actions={
          editable ? (
            (actions ?? (
              <>
                <Button variant="primary" onClick={() => open("schedule")} title="New schedule (N)" aria-keyshortcuts="n">
                  New schedule
                </Button>
                <MoreMenu hasApps={apps.length > 0} open={open} />
                <ConnectButton part="jobs" project={project} />
              </>
            ))
          ) : (
            <ConnectButton part="jobs" project={project} />
          )
        }
      >
        <JobsTabs project={project} tab={tab} />
      </PageHeader>
      {children}
      <Dialog open={!!Form} onOpenChange={(o) => !o && open(undefined)}>
        <DialogContent className="sm:max-w-2xl" aria-describedby={undefined}>
          {Form && (
            <Suspense fallback={<div className="h-80" />}>
              <Form project={project} name={search.name} done={() => open(undefined)} />
            </Suspense>
          )}
        </DialogContent>
      </Dialog>
    </Page>
  );
}

function MoreMenu({ hasApps, open }: { hasApps: boolean; open: (d: JobsDo) => void }) {
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button variant="secondary" size="icon" aria-label="More ways to add a job">
          <ChevronDown />
        </Button>
      </MenuTrigger>
      <MenuContent align="end" className="min-w-60">
        <MenuItem onSelect={() => open("schedule")}>
          New schedule <kbd className="kbd ml-auto">N</kbd>
        </MenuItem>
        <MenuItem onSelect={() => open("queue")}>
          New queue <kbd className="kbd ml-auto">U</kbd>
        </MenuItem>
        <MenuItem onSelect={() => open("send")}>
          Send a test job <kbd className="kbd ml-auto">T</kbd>
        </MenuItem>
        <MenuItem disabled={!hasApps} onSelect={() => open("run")}>
          Start a workflow run <kbd className="kbd ml-auto">W</kbd>
        </MenuItem>
      </MenuContent>
    </Menu>
  );
}

/** Runs · Schedules · Queues · Workers · Failed, each with its count: what runs now, how many, and what failed (in red). */
function JobsTabs({ project, tab }: { project: string; tab: JobsTab }) {
  const stats = useQuery({ ...jq.stats(project), retry: retryUnlessDown });
  const crons = useQuery({ ...jq.crons(project), retry: retryUnlessDown });
  const runs = useQuery({ ...jq.runs(project), retry: retryUnlessDown });
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const all = stats.data ?? [];
  const failedRuns = (runs.data ?? []).filter((r) => r.state === "failed").length;
  const dead = all.reduce((n, x) => n + x.dead, 0) + failedRuns;
  const running = all.reduce((n, x) => n + x.running, 0);
  const handlers = handlersOf(manifest.data?.manifest.apps, all, crons.data ?? [], runs.data ?? []).length;
  const count: Partial<Record<JobsTab, number | undefined>> = {
    runs: stats.isSuccess ? running : undefined,
    schedules: crons.data?.length,
    queues: stats.isSuccess ? ownQueues(all).length : undefined,
    workers: stats.isSuccess && manifest.isSuccess ? handlers : undefined,
    failed: stats.isSuccess ? dead : undefined,
  };
  return (
    <nav className="mt-7 -mb-px flex gap-0.5 overflow-x-auto border-b border-rule [scrollbar-width:none] sm:gap-1" aria-label="Jobs">
      {tabs.map((t) => {
        const n = count[t.tab];
        return (
          <Link
            key={t.tab}
            to={t.to as "/"}
            params={{ project, ...t.params } as never}
            search={{} as never}
            aria-current={t.tab === tab ? "page" : undefined}
            className={cn(
              "relative flex h-10 shrink-0 items-center gap-1.5 px-2 text-[0.875rem] transition-colors first:pl-0 hover:text-ink sm:px-3",
              "after:absolute after:inset-x-2 after:-bottom-px after:h-[2px] after:rounded-full first:after:left-0",
              t.tab === tab ? "font-[550] text-ink after:bg-ink" : "after:bg-transparent text-ink-3",
            )}
          >
            {t.label}
            {t.tab === "runs" && n ? (
              <span className="inline-flex items-center gap-1 text-xs font-normal text-ink-2 tnum" aria-label={`${n} running now`}>
                <span aria-hidden className="size-1.5 rounded-full bg-brass" />
                {int(n)}
              </span>
            ) : t.tab === "failed" ? (
              n ? (
                <span className="rounded-full bg-danger-wash px-1.5 text-xs font-[550] text-danger tnum" aria-label={`${n} failed`}>
                  {int(n)}
                </span>
              ) : null
            ) : n && t.tab !== "runs" ? (
              <span className="text-xs font-normal text-ink-3 tnum max-sm:hidden">{int(n)}</span>
            ) : null}
          </Link>
        );
      })}
    </nav>
  );
}

/** A small caps label over a group of rows. */
export function Label({ children, id, action }: { children: ReactNode; id?: string; action?: ReactNode }) {
  return (
    <div className="mb-2.5 flex min-h-7 items-end justify-between gap-3">
      <h2 id={id} className="label">
        {children}
      </h2>
      {action}
    </div>
  );
}

const secFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
export const clockSec = (iso: string) => secFmt.format(new Date(iso));
export const elapsed = (a?: string, b?: string) => (a && b ? new Date(b).getTime() - new Date(a).getTime() : undefined);

/** The owner's own name, for actors the API records as "owner (owner)": loads the people list (pages call it so they re-render once it's here). */
export function useOwnerName() {
  return useQuery({ ...api.people, retry: false, staleTime: 300_000 }).data?.find((p) => p.role === "owner")?.name;
}
const cachedOwner = () => queryClient.getQueryData(api.people.queryKey)?.find((p) => p.role === "owner")?.name;

/** "owner (owner)" → "Sam", "claude-code (agent)" → "Claude Code", "workflow" → "a workflow". */
export function whoWords(s?: string) {
  if (!s) return undefined;
  const m = s.match(/^(.*) \((\w+)\)(?: session .*)?$/);
  if (m && (m[2] === "owner" || (m[2] === "human" && m[1].toLowerCase() === "owner")) && cachedOwner()) return cachedOwner();
  if (m) return actorWords({ kind: m[2], name: m[1] });
  if (s === "workflow") return "a workflow turn";
  if (s === "cron") return "its schedule";
  return s;
}

/** A workflow's history line in words: local clock times, real plurals, a result as "status shipped, by Sam". */
export function timelineWords(m: string) {
  return m
    .replace(/\b(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)\b/g, (iso) => clockSec(iso))
    .replace(/\b(\d+) turn\(s\)/g, (_, n) => (n === "1" ? "one turn" : `${words(Number(n))} turns`))
    .replace(/→\s*(\{.*\})\s*$/, (whole, json) => {
      try {
        const o = JSON.parse(json) as Record<string, unknown>;
        const parts = Object.entries(o).map(([k, v]) => (k === "by" ? `by ${whoWords(String(v)) ?? v}` : `${k} ${typeof v === "object" ? JSON.stringify(v) : String(v)}`));
        return `→ ${parts.join(", ")}`;
      } catch {
        return whole;
      }
    });
}

export function Json({ title, value, label }: { title: string; value: unknown; label: string }) {
  return (
    <div>
      <Label>{title}</Label>
      <Untrusted label={label}>
        <pre className="max-h-80 overflow-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2" tabIndex={0}>
          {value === undefined ? "(nothing)" : JSON.stringify(value, null, 2)}
        </pre>
      </Untrusted>
    </div>
  );
}

/** "hooks.example.com/digest": a URL without its scheme, for rows. */
export const shortURL = (u: string) => u.replace(/^https?:\/\//, "").replace(/\/$/, "");

/** Where a schedule or queue delivers, in a few words. */
export function TargetWords({ app, path, url, className }: { app?: string; path?: string; url?: string; className?: string }) {
  if (url)
    return (
      <span className={cn("font-mono text-[0.72rem]", className)} title={url}>
        → {shortURL(url)}
      </span>
    );
  if (app)
    return (
      <span className={cn("font-mono text-[0.72rem]", className)}>
        → {app} {path}
      </span>
    );
  return <span className={className}>nowhere yet</span>;
}
