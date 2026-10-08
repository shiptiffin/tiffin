import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type Backup, type BackupDrill, type BackupOffsite, type BackupOffsiteTest, type BackupRestored, type OffsiteInput } from "@/api/modules";
import { Throttle } from "@/components/throttle";
import { cn } from "@/lib/cn";
import { q } from "@/api/queries";
import { Breaker } from "@/components/breaker";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import { Calm, Group, healthCrumbs, Rows, Segmented, StateLine } from "@/components/health-kit";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { SegMeter } from "@/components/seg-meter";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/choice";
import { Input, Label } from "@/components/ui/input";
import { Confirm } from "@/components/confirm";
import { CopyValue } from "@/components/copy";
import { bytes, count, countWords, dec, duration, int, ms, pct, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, dayLabel, full, relative } from "@/lib/time";

type Preview = {
  backup: string;
  takenAt: string;
  targets: string[];
  overwrites: Array<{ target: string; what: string; items?: string[] }>;
  safety: string;
  downtime: string;
};
type Schedule = { enabled: boolean; fullEveryHours: number; incrementalEveryHours: number; retainFull: number; drillEnabled: boolean; drillEveryDays: number };

const targetCopy: Record<string, string> = { postgres: "Postgres", valkey: "Valkey", files: "Files (storage and mail)" };
const H = 3600_000;

function total(b: Backup) {
  return (b.postgres?.sizeBytes ?? 0) + (b.valkey?.sizeBytes ?? 0) + Object.values(b.files ?? {}).reduce((n, f) => n + f.sizeBytes, 0);
}
/** What a backup added to the repository: Postgres's compressed delta plus the copied files. */
function stored(b: Backup) {
  return (b.postgres?.repoBytes ?? 0) + (b.valkey?.sizeBytes ?? 0) + Object.values(b.files ?? {}).reduce((n, f) => n + f.sizeBytes, 0);
}
const every = (h: number) => (h === 1 ? "every hour" : h === 24 ? "every day" : h === 168 ? "every week" : h % 24 === 0 ? `every ${h / 24} days` : `every ${h} hours`);
/** Seconds in words, to a tenth under ten: "0.3 s", "14 s", "2 min". */
const secs = (n: number) => (n < 0.1 ? "under 0.1\u202Fs" : n < 10 ? `${dec(n, 1)}\u202Fs` : duration(n));
const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s).replace(/\d{4}-\d\d-\d\dT[\d:.]+Z/g, (t) => full(t));

/**
 * Backups: when the box was last backed up and when it will be next, then
 * the history with its restores (through the guard that makes you read what
 * is overwritten), copies off the box, and, one step away, the schedule, the
 * space it takes and the restore drill.
 */
export function BackupsPage() {
  useTitle("Backups");
  const qc = useQueryClient();
  const o = useQuery(mq.backups);
  const res = useQuery(q.resources);
  const { me } = useMe();
  const owner = me?.role === "owner" || me?.kind === "owner";
  const run = useMutation({
    mutationFn: (kind: "full" | "incremental") => mod.backupNow(kind),
    onSuccess: () => toast({ title: "Backing up the whole box now. It shows in the history when it’s done." }),
    onSettled: () => qc.invalidateQueries({ queryKey: ["backups"] }),
  });
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [targets, setTargets] = useState<string[]>(["postgres", "valkey"]);
  const [done, setDone] = useState<BackupRestored | null>(null);

  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Backups" />;
  if (o.isPending)
    return (
      <Page wide>
        <Skeleton className="h-4 w-20" />
        <Skeleton className="mt-3 h-8 w-44" />
        <Skeleton className="mt-6 h-7 w-[32rem] max-w-full" />
      </Page>
    );
  if (o.isError)
    return (
      <Page wide>
        <PageHeader eyebrow={healthCrumbs} title="Backups" />
        <ProblemNote className="mt-8" error={o.error} />
      </Page>
    );
  const d = o.data;
  const list = d.backups ?? [];
  const sch = d.schedule as Schedule;
  const now = o.dataUpdatedAt;
  const last = d.lastOkAt;
  const stale = !last || now - new Date(last).getTime() > 26 * H;
  const running = list.find((b) => b.status === "running");
  const ok = list.filter((b) => b.status === "ok");
  const lastFull = ok.find((b) => b.kind === "full");
  const nextFull = lastFull ? new Date(lastFull.startedAt).getTime() + sch.fullEveryHours * H : now;
  const nextInc = sch.incrementalEveryHours > 0 && ok[0] ? new Date(ok[0].startedAt).getTime() + sch.incrementalEveryHours * H : Infinity;
  const next = Math.min(nextFull, nextInc);
  const nextKind = next === nextFull ? "a full one" : "changes only";
  const nextWords = next <= now + 60_000 ? `The next one, ${nextKind}, is due now.` : `The next one, ${nextKind}, runs at about ${clock(new Date(next).toISOString())}${dayLabel(new Date(next).toISOString()) === "Today" ? "" : ` ${dayLabel(new Date(next).toISOString()).toLowerCase()}`}.`;
  const restores = list.filter((b) => b.trigger === "pre-restore");
  const disk = res.data?.disks.data;

  let line: string;
  if (running) line = `Backing up now, started ${relative(running.startedAt, now)}.`;
  else if (!last) line = "Nothing has been backed up yet.";
  else line = `Backed up ${relative(last, now)}. ${sch.enabled ? nextWords : "Automatic backups are off."}`;
  const ld = d.lastDrill as BackupDrill | null | undefined;
  const drillLine = !ld
    ? " No restore drill yet."
    : ld.status === "running"
      ? " A restore drill is running."
      : ld.status === "passed"
        ? ` Last restore drill passed ${relative(ld.finishedAt ?? ld.startedAt, now)}, restored in ${secs(ld.seconds.restore)}.`
        : ` The last restore drill failed ${relative(ld.finishedAt ?? ld.startedAt, now)}.`;

  return (
    <Page wide>
      <PageHeader
        eyebrow={healthCrumbs}
        title="Backups"
        actions={
          owner && (
            <Button variant="primary" size="lg" onClick={() => run.mutate("full")} disabled={run.isPending || !!running}>
              {running ? <PilotLight state="busy" /> : null}
              {running || run.isPending ? "Backing up…" : "Back up now"}
            </Button>
          )
        }
      />
      <StateLine danger={(stale && !running) || ld?.status === "failed"}>
        {line}
        {drillLine}
      </StateLine>
      {run.isError && <ProblemNote className="mt-6" error={run.error} />}
      {done && (
        <p className="mt-6 max-w-[48rem] text-[0.875rem] text-ink">
          Restored {done.targets?.map((t) => targetCopy[t] ?? t).join(" and ")} from {done.backup} in {ms(done.durationMs)}. If that was a mistake, restore{" "}
          <code className="ident">{done.safetyBackup}</code>: it’s what was there a moment ago.
        </p>
      )}

      <Group label="History" id="history" aside={list.length ? `keeps ${words(sch.retainFull)} full backups and what they need` : undefined}>
        {list.length === 0 ? (
          <Calm art="backups" title="Nothing backed up yet." className="border-y border-rule">
            {owner ? "Back up now, so there’s something to go back to. After that the schedule takes over." : "The owner can take the first one."}
          </Calm>
        ) : (
          <Rows>
            {list.map((b) => (
              <li key={b.id} className="grid grid-cols-[4.5rem_minmax(0,1fr)_auto] items-center gap-x-4 py-2.5">
                <span className="text-[0.78125rem] leading-4 text-ink-3 tnum" title={full(b.startedAt)}>
                  {clock(b.startedAt)}
                  <span className="block">{dayLabel(b.startedAt) === "Today" ? "today" : dayLabel(b.startedAt).replace(/,.*$/, "")}</span>
                </span>
                <span className="min-w-0">
                  <span className="flex flex-wrap items-center gap-x-2 text-[0.875rem] text-ink">
                    {b.status === "running" && <PilotLight state="busy" label="Running" />}
                    {b.kind === "full" ? "Full backup" : "Changes since the last one"}
                    <span className="text-[0.8125rem] text-ink-3">
                      {b.trigger === "pre-restore" ? "safety copy before a restore" : b.trigger === "manual" ? "taken by hand" : "on schedule"}
                      {b.offsite?.status === "ok" ? " · copied off the box" : b.offsite?.status === "failed" ? " · not copied off the box" : ""}
                    </span>
                  </span>
                  <span className="mt-0.5 block text-[0.8125rem] text-ink-3 sm:truncate">
                    {b.status === "running" ? (
                      "Running…"
                    ) : b.status === "failed" ? (
                      <span className="text-danger">Failed: {b.error}</span>
                    ) : (
                      <>
                        {/* What this backup stored (pgBackRest's compressed delta), and what it restores to. */}
                        {b.postgres?.repoBytes !== undefined ? (
                          <>
                            Stored {bytes(stored(b))}
                            {b.kind !== "full" ? " of changes" : ""}, restores {bytes(total(b))} · took {ms(b.durationMs)}
                          </>
                        ) : (
                          <>
                            {bytes(total(b))} · took {ms(b.durationMs)}
                          </>
                        )}
                      </>
                    )}
                  </span>
                </span>
                {owner && b.status === "ok" ? (
                  <Button variant="ghost" size="sm" onClick={() => setRestoring(b)}>
                    Restore…
                  </Button>
                ) : (
                  <span />
                )}
              </li>
            ))}
          </Rows>
        )}
      </Group>

      <OffsiteBlock o={d.offsite} owner={owner} now={now} />

      {/* The settings, one step away. Open from the start when something there needs a look: a drill running or failed, a nearly full disk. */}
      <SettingsFold open={ld?.status === "running" || ld?.status === "failed" || (!!disk && disk.usedPercent >= 80)}>
        <ScheduleLevers sch={sch} owner={owner} className="mt-6" />

        <Group label="Space" id="space">
          <Rows>
            <li className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-2 py-3 sm:grid-cols-[14rem_minmax(0,1fr)_9rem]">
              <div>
                <p className="text-[0.875rem] text-ink">Data disk</p>
                <p className="ident text-[0.71875rem] text-ink-3">{(d.destinations ?? [])[0]?.replace(/^local: /, "")}</p>
              </div>
              {disk ? (
                <div className="col-span-2 row-start-2 max-w-[26rem] sm:col-span-1 sm:row-start-auto">
                  <SegMeter value={disk.usedPercent} warnAt={0.8} fullAt={0.95} scale label="Data disk in use" valueText={`${dec(disk.usedPercent, 0)} percent`} />
                  <p className="mt-1.5 text-[0.8125rem] text-ink-3">
                    The disk is {dec(disk.usedPercent, 0)}&#8239;% full with {bytes(disk.freeBytes)} free; backups are {pct(d.repoBytes / disk.totalBytes, d.repoBytes / disk.totalBytes < 0.01 ? 1 : 0)} of
                    it, with the log Postgres needs to replay between them.
                  </p>
                </div>
              ) : (
                <p className="col-span-2 row-start-2 text-[0.8125rem] text-ink-3 sm:col-span-1 sm:row-start-auto">Compressed and deduplicated: each backup stores only what changed.</p>
              )}
              <p className="col-start-2 row-start-1 text-right text-[0.875rem] text-ink tnum sm:col-start-auto sm:row-start-auto">
                {bytes(d.repoBytes)}
                <span className="block text-xs text-ink-3">for {countWords(ok.length, "backup")}</span>
              </p>
            </li>
          </Rows>
        </Group>

        <Group label="Restore drill" id="drill" aside={restores.length ? `last real restore ${relative(restores[0].startedAt, now)}` : undefined}>
          <DrillBlock last={ld} owner={owner} hasBackup={!!ok[0]} />
        </Group>
      </SettingsFold>

      <HazardDialog<Preview, BackupRestored>
        key={restoring?.id + targets.join()}
        open={!!restoring}
        onOpenChange={(open) => !open && setRestoring(null)}
        title={restoring ? `Restore the backup from ${relative(restoring.startedAt)}?` : "Restore this backup?"}
        word="restore"
        action="Overwrite with the backup"
        run={(confirm) => mod.restore(restoring!.id, targets, confirm)}
        renderPreview={(p) => (
          <div className="flex flex-col gap-4">
            <fieldset>
              <legend className="mb-2 text-[0.8125rem] text-ink-3">Put back</legend>
              <div className="flex flex-wrap gap-x-5 gap-y-2">
                {["postgres", "valkey", "files"].map((t) => (
                  <label key={t} className="flex items-center gap-2 text-[0.875rem] text-ink">
                    <Checkbox checked={targets.includes(t)} onCheckedChange={(v) => setTargets((x) => (v === true ? [...x, t] : x.filter((y) => y !== t)))} />
                    {targetCopy[t]}
                  </label>
                ))}
              </div>
            </fieldset>
            <div className="border-y border-danger-rule py-3">
              <p className="text-[0.875rem] font-[550] text-danger">Everything since {relative(p.takenAt)} is lost:</p>
              <ul className="mt-2 flex flex-col gap-2 text-[0.84375rem]">
                {p.overwrites.map((x) => (
                  <li key={x.target} className="text-ink-2">
                    <span className="font-[550] text-ink">{targetCopy[x.target] ?? x.target}:</span> {cap(x.what)}
                    {x.items && x.items.length > 0 && <span className="ident mt-1 block text-ink-3">{x.items.join(" · ")}</span>}
                  </li>
                ))}
              </ul>
            </div>
            <p className="text-[0.84375rem] text-ink-2">
              {cap(p.safety)}. {cap(p.downtime)}.
            </p>
          </div>
        )}
        onDone={(r) => {
          setDone(r);
          setRestoring(null);
          qc.invalidateQueries();
        }}
      />
    </Page>
  );
}

/**
 * Schedule, space and the restore drill, behind one closed disclosure. `open`
 * is read once, when the page mounts, so a drill finishing doesn't fold it
 * away under you.
 */
function SettingsFold({ open, children }: { open: boolean; children: ReactNode }) {
  const [initial] = useState(open);
  return (
    <details className="group mt-11" open={initial}>
      <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
        <span className="inline-block transition-transform group-open:rotate-90">›</span> Schedule, space and restore drills
      </summary>
      {children}
    </details>
  );
}

/** The schedule as levers: a breaker for automatic backups, detents for how often and how many. Applies straight away, with Undo. */
function ScheduleLevers({ sch, owner, className }: { sch: Schedule; owner: boolean; className?: string }) {
  const qc = useQueryClient();
  const set = useMutation({
    mutationFn: (next: Partial<Schedule>) => mod.setSchedule({ ...sch, ...next }),
    onSuccess: (_r, next) => {
      qc.invalidateQueries({ queryKey: ["backups"] });
      const before = sch;
      const what =
        next.enabled !== undefined
          ? next.enabled
            ? "Automatic backups are on."
            : "Automatic backups are off. Only backups you take by hand from now on."
          : next.fullEveryHours !== undefined
            ? `A full backup ${every(next.fullEveryHours)} from now on.`
            : next.incrementalEveryHours !== undefined
              ? next.incrementalEveryHours
                ? `The changes are saved ${every(next.incrementalEveryHours)}.`
                : "No backups of changes between full ones."
              : next.drillEnabled !== undefined
                ? next.drillEnabled
                  ? `A restore drill runs ${everyDays(sch.drillEveryDays)} from now on.`
                  : "No automatic restore drills. Run one by hand below."
                : next.drillEveryDays !== undefined
                  ? `A restore drill runs ${everyDays(next.drillEveryDays)} from now on.`
                  : `Keeping the last ${words(next.retainFull ?? sch.retainFull)} full backups.`;
      toast({ title: what, action: { label: "Undo", run: () => mod.setSchedule(before).then(() => qc.invalidateQueries({ queryKey: ["backups"] })) } });
    },
  });
  const dis = !owner || set.isPending;
  return (
    <Group label="Schedule" id="schedule" className={className} aside={owner ? undefined : "The owner sets the schedule"}>
      <Rows>
        <Lever
          lever={<Breaker label="Automatic backups" state={sch.enabled ? "on" : "off"} disabled={dis} onFlip={(v) => set.mutate({ enabled: v === "on" })} />}
          name="Automatic backups"
          status={
            sch.enabled ? (
              `On: a full backup ${every(sch.fullEveryHours)}${sch.incrementalEveryHours ? `, and the changes ${every(sch.incrementalEveryHours)}` : ""}.`
            ) : (
              <span className="text-warn-ink">Off: only backups you take by hand.</span>
            )
          }
        />
        <Lever
          name="Full backup"
          status="The whole box, from scratch."
          control={
            <Segmented
              label="Full backup every"
              disabled={dis || !sch.enabled}
              value={String(sch.fullEveryHours)}
              onChange={(v) => set.mutate({ fullEveryHours: Number(v) })}
              options={[12, 24, 48, 168].map((h) => ({ v: String(h), label: h === 12 ? "12 h" : h === 24 ? "Daily" : h === 48 ? "2 days" : "Weekly" }))}
            />
          }
        />
        <Lever
          name="Changes in between"
          status="Small and quick: only what changed since the last one."
          control={
            <Segmented
              label="Save changes every"
              disabled={dis || !sch.enabled}
              value={String(sch.incrementalEveryHours)}
              onChange={(v) => set.mutate({ incrementalEveryHours: Number(v) })}
              options={[0, 1, 4, 12].map((h) => ({ v: String(h), label: h === 0 ? "Off" : `${h} h` }))}
            />
          }
        />
        <Lever
          name="Keep"
          status="Older full backups, and the changes that hang off them, are pruned."
          control={
            <Segmented
              label="Full backups to keep"
              disabled={dis}
              value={String(sch.retainFull)}
              onChange={(v) => set.mutate({ retainFull: Number(v) })}
              options={[3, 7, 14, 30].map((n) => ({ v: String(n), label: String(n) }))}
            />
          }
        />
        <Lever
          lever={<Breaker label="Automatic restore drills" state={sch.drillEnabled ? "on" : "off"} disabled={dis} onFlip={(v) => set.mutate({ drillEnabled: v === "on" })} />}
          name="Restore drills"
          status={
            sch.drillEnabled ? (
              `Proves the newest backup restores, ${everyDays(sch.drillEveryDays)}, without touching anything live.`
            ) : (
              <span className="text-warn-ink">Off: backups are only checked when you run a drill.</span>
            )
          }
          control={
            <span className="flex items-center gap-2.5">
              <Throttle
                size="mini"
                label="Restore drill every so many days"
                unit="days"
                stops={DRILL_STOPS}
                value={sch.drillEveryDays}
                applied={sch.drillEveryDays}
                onCommit={(v) => !dis && sch.drillEnabled && set.mutate({ drillEveryDays: v })}
              />
              <span className="w-16 text-[0.8125rem] text-ink-2 tnum">{everyDays(sch.drillEveryDays).replace("every ", "")}</span>
            </span>
          }
        />
      </Rows>
      {set.isError && <ProblemNote className="mt-3" error={set.error} />}
    </Group>
  );
}

function Lever({ lever, name, status, control }: { lever?: ReactNode; name: string; status: ReactNode; control?: ReactNode }) {
  return (
    <li className="grid grid-cols-[2rem_minmax(0,1fr)] items-center gap-x-3 gap-y-2 py-2.5 sm:grid-cols-[2rem_11rem_minmax(0,1fr)_auto] sm:gap-x-4">
      <span className="flex">{lever}</span>
      <span className="text-[0.875rem] text-ink">{name}</span>
      <span className="col-start-2 text-[0.84375rem] text-ink-2 sm:col-start-auto">{status}</span>
      {control && <span className="col-start-2 sm:col-start-auto">{control}</span>}
    </li>
  );
}

const DRILL_STOPS = [1, 3, 7, 14, 30];
const everyDays = (n: number) => (n === 1 ? "every day" : n === 7 ? "every week" : `every ${n} days`);

/**
 * The restore drill: proves a backup restores, into a scratch copy, without
 * touching anything live. Shows the phases while it runs, then a receipt:
 * how long each step took and every database's tables and rows.
 */
function DrillBlock({ last, owner, hasBackup }: { last?: BackupDrill | null; owner: boolean; hasBackup: boolean }) {
  const qc = useQueryClient();
  const [id, setId] = useState<string | undefined>(undefined);
  const watching = id ?? (last?.status === "running" ? last.id : undefined);
  const live = useQuery({
    queryKey: ["drill", watching ?? ""],
    queryFn: () => mod.drillGet(watching!),
    enabled: !!watching,
    refetchInterval: (qq) => (qq.state.data?.status === "running" || !qq.state.data ? 1500 : false),
    refetchIntervalInBackground: true,
  });
  const start = useMutation({
    mutationFn: () => mod.drill(),
    onSuccess: (d) => {
      setId(d.id);
      qc.setQueryData(["drill", d.id], d);
    },
  });
  const cancel = useMutation({
    mutationFn: (x: string) => mod.drillCancel(x),
    onSuccess: (d) => qc.setQueryData(["drill", d.id], d),
  });
  const drill = (watching ? live.data : undefined) ?? last ?? undefined;
  const runningNow = drill?.status === "running";
  // When a drill we watched finishes, the overview's lastDrill and the page's sentence catch up.
  const finished = !!id && !!live.data && live.data.status !== "running";
  useEffect(() => {
    if (finished) void qc.invalidateQueries({ queryKey: ["backups"] });
  }, [finished, qc]);

  return (
    <div className="border-y border-rule py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-[44rem] text-[0.84375rem] text-ink-2">Restores the newest backup into a scratch copy and counts every table. Nothing live is touched.</p>
        {owner && hasBackup && !runningNow && (
          <Button size="md" onClick={() => start.mutate()} disabled={start.isPending}>
            {start.isPending ? "Starting…" : "Run a restore drill"}
          </Button>
        )}
        {owner && runningNow && (
          <Button size="md" variant="ghost" onClick={() => cancel.mutate(drill.id)} disabled={cancel.isPending}>
            Cancel the drill
          </Button>
        )}
      </div>
      {start.isError && <ProblemNote className="mt-3" error={start.error} />}
      {runningNow && drill && <DrillProgress d={drill} />}
      {drill && !runningNow && <DrillReceipt d={drill} />}
    </div>
  );
}

const steps: Array<{ key: string; words: string; phases: string[] }> = [
  { key: "restore", words: "Restore the backup into a scratch copy", phases: ["", "queued", "restore", "restoring"] },
  { key: "start", words: "Start a private Postgres on it", phases: ["start", "starting"] },
  { key: "verify", words: "Count every table of every database", phases: ["verify", "verifying", "counting"] },
  { key: "cleanup", words: "Throw the copy away", phases: ["cleanup", "cleaning"] },
];

/** The drill's steps as a checklist: done ✓, the current one with the pilot light, the rest waiting. */
function DrillProgress({ d }: { d: BackupDrill }) {
  const at = Math.max(0, steps.findIndex((x) => x.phases.includes(d.phase)));
  return (
    <ol className="mt-4 max-w-[36rem]" role="status" aria-live="polite" aria-label="Restore drill progress">
      {steps.map((x, i) => (
        <li key={x.key} className={cn("grid grid-cols-[1.25rem_minmax(0,1fr)_auto] items-center gap-x-2 py-1.5 text-[0.875rem]", i > at ? "text-ink-3" : "text-ink")}>
          <span className="grid place-items-center">{i < at ? <span className="text-ok">✓</span> : i === at ? <PilotLight state="busy" label="Running" /> : <span className="pilot" data-state="off" />}</span>
          <span>{x.words}</span>
          <span className="text-xs text-ink-3 tnum">
            {i === 0 && i === at && d.backupBytes > 0 ? `${int(d.percent)} % · ${bytes(d.restoredBytes)} of ${bytes(d.backupBytes)}` : ""}
          </span>
        </li>
      ))}
    </ol>
  );
}

/** The result as a receipt: an object, so it gets a box. */
function DrillReceipt({ d }: { d: BackupDrill }) {
  const passed = d.status === "passed";
  const dbs = d.databases ?? [];
  return (
    <article className={cn("mt-4 max-w-[46rem] rounded-[10px] border bg-paper-raised px-4 py-3.5", passed ? "border-rule-2" : "border-danger")}>
      <header className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <p className={cn("text-[0.9375rem] font-[550]", passed ? "text-ink" : "text-danger")}>
          {passed ? "✓ The backup restores." : "× The drill failed."}{" "}
          <span className="font-[400] text-ink-3">
            {d.trigger === "schedule" ? "On schedule" : "Run by hand"}{d.source === "offsite" ? ", from the off-box copy" : ""}, {relative(d.finishedAt ?? d.startedAt)}
          </span>
        </p>
        <span className="ident text-[0.71875rem] text-ink-3">{d.backupLabel || d.backup}</span>
      </header>
      {passed ? (
        <p className="mt-1.5 text-[0.84375rem] text-ink-2">
          Restored in <b className="font-[550] text-ink">{secs(d.seconds.restore)}</b> · started in <b className="font-[550] text-ink">{secs(d.seconds.start)}</b> ·
          verified in <b className="font-[550] text-ink">{secs(d.seconds.verify)}</b>. The backup was taken {duration(d.backupAgeSeconds)} before the drill (
          {bytes(d.backupBytes)}).
        </p>
      ) : (
        <p className="mt-1.5 text-[0.84375rem] text-danger">
          {cap(d.message)}
          {d.hint && <span className="mt-1 block text-ink-2">{cap(d.hint)}</span>}
        </p>
      )}
      {dbs.length > 0 && (
        <ul className="mt-3 divide-y divide-rule border-t border-rule">
          {dbs.map((db) => (
            <li key={db.name} className="grid grid-cols-[1.25rem_minmax(0,1fr)_auto] items-baseline gap-x-2 py-2 text-[0.84375rem]">
              <span className={db.ok ? "text-ok" : "text-danger"} aria-label={db.ok ? "complete" : "incomplete"}>
                {db.ok ? "✓" : "×"}
              </span>
              <span className="min-w-0">
                <span className="ident text-ink">{db.name.replace(/^p_/, "")}</span>
                {(db.missing?.length ?? 0) > 0 && <span className="block text-danger">Missing: {db.missing!.join(", ")}</span>}
                {(db.problems?.length ?? 0) > 0 && <span className="block text-danger">Couldn’t read: {db.problems!.join(", ")}</span>}
              </span>
              <span className="text-ink-2 tnum">
                {count(db.tables, "table")}, {count(db.rows, "row")}
              </span>
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}

/**
 * Copies off the box: whether every backup also goes, encrypted, to a
 * bucket elsewhere. Off, it says so plainly (the backups live and die with
 * this server) and offers the form; on, the newest copy, Test, Copy now and
 * Turn off. A new destination's passphrase is shown once, to keep.
 */
function OffsiteBlock({ o, owner, now }: { o?: BackupOffsite | null; owner: boolean; now: number }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const [pass, setPass] = useState<string | null>(null);
  const [tested, setTested] = useState<BackupOffsiteTest | null>(null);
  const refresh = () => qc.invalidateQueries({ queryKey: ["backups"] });
  const test = useMutation({ mutationFn: mod.offsiteTest, onSuccess: setTested });
  const copy = useMutation({
    mutationFn: mod.offsiteCopy,
    onSuccess: (c) => toast({ title: c.status === "ok" ? `Copied off the box: ${bytes(c.sentBytes)} sent.` : "Copying off the box now. It shows here when it’s done." }),
    onSettled: refresh,
  });
  const on = !!o?.enabled;
  const last = o?.lastOk;
  const failing = o?.lastCopy?.status === "failed" ? o.lastCopy : null;
  const stale = on && (!o?.lastOkAt || now - new Date(o.lastOkAt).getTime() > 26 * H);

  return (
    <Group label="Copies off the box" id="offsite" aside={on ? (o?.state === "foreign" ? "paused" : "on") : "off"}>
      {pass && (
        <div className="mb-4 max-w-[46rem] rounded-[10px] border border-brass bg-paper-raised px-4 py-3.5" role="alert">
          <p className="text-[0.9375rem] font-[550] text-ink">Keep this passphrase somewhere safe, off this server.</p>
          <p className="mt-1 text-[0.84375rem] text-ink-2">
            The copies are encrypted with it. If this server is lost, a new box needs it to restore them. It’s shown only now.
          </p>
          <div className="mt-2.5 flex flex-wrap items-center gap-3">
            <CopyValue value={pass} className="text-[0.9375rem]" />
            <Button size="sm" variant="ghost" onClick={() => setPass(null)}>
              I’ve saved it
            </Button>
          </div>
        </div>
      )}
      {!on ? (
        <div className="border-y border-rule py-3.5">
          <p className="flex items-start gap-2 text-[0.9375rem] font-[550] text-warn-ink">
            <TriangleAlert className="mt-0.5 size-4 shrink-0" />
            Backups only on this server.
          </p>
          <p className="mt-1 max-w-[44rem] text-[0.84375rem] text-ink-2">
            They undo mistakes, not the loss of the machine: if it goes, they go with it. Copy every backup, encrypted, to a bucket you own (Cloudflare R2, AWS
            S3, Hetzner Object Storage, MinIO), and a new box can bring everything back.
          </p>
          {owner && !editing && (
            <Button className="mt-3" size="md" variant="primary" onClick={() => setEditing(true)}>
              Set a destination…
            </Button>
          )}
        </div>
      ) : (
        <div className="border-y border-rule py-3.5">
          <p className={cn("text-[0.875rem]", stale || o?.state === "foreign" ? "text-danger" : "text-ink")}>
            <span className="font-[550]">Copies off the box: on.</span>{" "}
            {o?.state === "foreign"
              ? "Paused: this folder holds another box’s backups. Restore them (tiffin restore latest --from offsite), or choose another folder."
              : last
                ? `Last copied ${relative(last.finishedAt ?? last.startedAt, now)}, ${bytes(last.sentBytes)} sent (${bytes((last.files?.bytes ?? 0) + (last.postgresBytes ?? 0))} in the set’s changes and files).`
                : "The first copy runs after the next backup."}
          </p>
          <p className="ident mt-0.5 text-[0.71875rem] text-ink-3">
            s3://{o?.bucket}/{o?.prefix} · {o?.endpoint?.replace(/^https:\/\//, "")} · kept {o?.retentionDays} days
          </p>
          {failing && <p className="mt-1.5 text-[0.8125rem] text-danger">The latest copy failed: {failing.error}</p>}
          {o?.copying && <p className="mt-1.5 text-[0.8125rem] text-ink-2">Copying {o.copying} now…</p>}
          {owner && (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <Button size="sm" onClick={() => copy.mutate()} disabled={copy.isPending || !!o?.copying || o?.state !== "active"}>
                {copy.isPending ? "Copying…" : "Copy now"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => test.mutate()} disabled={test.isPending}>
                {test.isPending ? "Testing…" : "Test"}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setEditing((x) => !x)}>
                Change…
              </Button>
              <Button size="sm" variant="danger-quiet" onClick={() => setLeaving(true)}>
                Turn off…
              </Button>
            </div>
          )}
          {(test.isError || copy.isError) && <ProblemNote className="mt-3" error={test.error ?? copy.error} />}
          {tested && (
            <ul className="mt-3 max-w-[40rem] text-[0.8125rem]" aria-label="Destination test">
              {(tested.steps ?? []).map((s) => (
                <li key={s.name} className="grid grid-cols-[1.25rem_minmax(0,1fr)_auto] gap-x-2 py-0.5">
                  <span className={s.ok ? "text-ok" : "text-danger"}>{s.ok ? "✓" : "×"}</span>
                  <span className={cn("min-w-0 break-words", s.ok ? "text-ink-2" : "text-danger")}>
                    {s.name}
                    {s.detail ? `: ${s.detail}` : ""}
                  </span>
                  <span className="text-ink-3 tnum">{ms(s.ms)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {owner && editing && (
        <OffsiteForm
          o={on ? o : null}
          onDone={(r) => {
            setEditing(false);
            if (r.passphrase) setPass(r.passphrase);
            toast({ title: r.state === "foreign" ? "Connected. The folder holds another box’s backups: restore them, or choose another folder." : "Copies off the box are on." });
            void refresh();
          }}
          onCancel={() => setEditing(false)}
        />
      )}
      <Confirm
        open={leaving}
        onClose={() => setLeaving(false)}
        title="Stop copying backups off the box?"
        body="The box forgets the bucket and its keys; backups stay on this server only. The copies already in the bucket stay there; keep the passphrase to restore them."
        action="Turn off"
        run={mod.offsiteOff}
        done={() => {
          void refresh();
          toast({ title: "Copies off the box are off. Backups are only on this server." });
        }}
      />
    </Group>
  );
}

const offsiteFields: Array<{ k: keyof OffsiteInput; label: string; hint: string; type?: string }> = [
  { k: "endpoint", label: "Endpoint", hint: "https://<account>.r2.cloudflarestorage.com" },
  { k: "region", label: "Region", hint: "auto for R2, us-east-1, fsn1…" },
  { k: "bucket", label: "Bucket", hint: "tiffin-backups (it must exist)" },
  { k: "prefix", label: "Folder", hint: "tiffin (one per box)" },
  { k: "accessKeyId", label: "Access key ID", hint: "" },
  { k: "secretAccessKey", label: "Secret access key", hint: "", type: "password" },
  { k: "passphrase", label: "Passphrase", hint: "only to use copies another box made", type: "password" },
];

function OffsiteForm({ o, onDone, onCancel }: { o: BackupOffsite | null; onDone: (r: BackupOffsite) => void; onCancel: () => void }) {
  const [v, setV] = useState<Record<string, string>>({
    endpoint: o?.endpoint ?? "",
    region: o?.region ?? "",
    bucket: o?.bucket ?? "",
    prefix: o?.prefix ?? "",
    accessKeyId: o?.accessKeyId ?? "",
    secretAccessKey: "",
    passphrase: "",
  });
  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, string> = {};
      for (const [k, x] of Object.entries(v)) if (x.trim()) body[k] = x.trim();
      return mod.offsiteSet(body as unknown as OffsiteInput);
    },
    onSuccess: onDone,
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className="mt-4 max-w-[46rem]"
    >
      <div className="grid gap-x-4 gap-y-3 sm:grid-cols-2">
        {offsiteFields.map((f) => (
          <div key={f.k} className="flex min-w-0 flex-col gap-1">
            <Label htmlFor={`off-${f.k}`}>{f.label}</Label>
            <Input
              id={`off-${f.k}`}
              type={f.type ?? "text"}
              value={v[f.k] ?? ""}
              onChange={(e) => setV((x) => ({ ...x, [f.k]: e.target.value }))}
              placeholder={f.k === "secretAccessKey" && o ? "unchanged" : f.hint}
              autoComplete="off"
              spellCheck={false}
              className="ident text-[0.875rem]"
            />
          </div>
        ))}
      </div>
      <p className="mt-3 text-xs text-ink-3">
        The box writes, reads and deletes a test object first, then keeps the keys encrypted. A new destination gets a passphrase, shown once.
      </p>
      {save.isError && <ProblemNote className="mt-3" error={save.error} />}
      <div className="mt-3 flex gap-2">
        <Button type="submit" variant="primary" size="md" disabled={save.isPending || !v.endpoint.trim() || !v.bucket.trim() || !v.accessKeyId.trim()}>
          {save.isPending ? "Checking the bucket…" : o ? "Save" : "Check and turn on"}
        </Button>
        <Button type="button" variant="ghost" size="md" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
