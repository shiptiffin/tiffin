import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, CalendarClock, HardDrive, Play, RotateCcw } from "lucide-react";
import { useState } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type Backup, type BackupRestored } from "@/api/modules";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import { Page, PageHeader, Skeleton, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { bytes, ms } from "@/lib/format";
import { useMe } from "@/lib/me";
import { full, relative } from "@/lib/time";

type Preview = {
  backup: string;
  takenAt: string;
  targets: string[];
  overwrites: Array<{ target: string; what: string; items?: string[] }>;
  safety: string;
  downtime: string;
};

const targetCopy: Record<string, string> = { postgres: "Postgres", valkey: "Valkey", files: "Files (storage and mail)" };

function total(b: Backup) {
  return (b.postgres?.sizeBytes ?? 0) + (b.valkey?.sizeBytes ?? 0) + Object.values(b.files ?? {}).reduce((n, f) => n + f.sizeBytes, 0);
}

export function BackupsPage() {
  useTitle("Backups");
  const qc = useQueryClient();
  const o = useQuery(mq.backups);
  const { me } = useMe();
  const owner = me?.role === "owner" || me?.kind === "owner";
  const run = useMutation({
    mutationFn: (kind: "full" | "incremental") => mod.backupNow(kind),
    onSettled: () => qc.invalidateQueries({ queryKey: ["backups"] }),
  });
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [targets, setTargets] = useState<string[]>(["postgres", "valkey"]);
  const [done, setDone] = useState<BackupRestored | null>(null);

  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Backups" />;
  if (o.isPending)
    return (
      <Page wide>
        <Skeleton className="h-10 w-56" />
        <Skeleton className="mt-8 h-28" />
      </Page>
    );
  if (o.isError)
    return (
      <Page wide>
        <ProblemNote error={o.error} />
      </Page>
    );
  const d = o.data;
  const list = d.backups ?? [];
  const sch = d.schedule;
  const last = d.lastOkAt;
  const stale = !last || o.dataUpdatedAt - new Date(last).getTime() > 26 * 3600_000;

  return (
    <Page wide>
      <PageHeader
        title="Backups"
        lede="The whole box (Postgres, Valkey, stored files, mail and the box's own state) on a schedule, so a bad day is an inconvenience, not a loss."
        actions={
          owner && (
            <Button variant="primary" onClick={() => run.mutate("full")} disabled={run.isPending}>
              <Play />
              {run.isPending ? "Backing up…" : "Back up now"}
            </Button>
          )
        }
      />
      {run.isError && <ProblemNote className="mt-6" error={run.error} />}

      <section className={cn("mt-8 grid gap-px overflow-hidden rounded-xl border bg-rule sm:grid-cols-3", stale ? "border-out/40" : "border-rule")}>
        <div className="bg-raised p-5">
          <p className="text-2xs font-medium tracking-wider text-ink-3 uppercase">Last good backup</p>
          <p className={cn("display mt-2 text-3xl", stale ? "text-out" : "text-ink")}>{last ? relative(last) : "Never"}</p>
          <p className="mt-1 text-sm text-ink-3">{last ? full(last) : "Run one now, so there's something to go back to."}</p>
        </div>
        <div className="bg-raised p-5">
          <p className="flex items-center gap-1.5 text-2xs font-medium tracking-wider text-ink-3 uppercase">
            <CalendarClock className="size-3.5" /> Schedule
          </p>
          <p className="mt-2 text-base text-ink">
            {sch.enabled ? (
              <>
                Full every {sch.fullEveryHours} h, changes every {sch.incrementalEveryHours} h
              </>
            ) : (
              "Off: only manual backups"
            )}
          </p>
          <p className="mt-1 text-sm text-ink-3">Keeps the last {sch.retainFull} full backups and what they need.</p>
        </div>
        <div className="bg-raised p-5">
          <p className="flex items-center gap-1.5 text-2xs font-medium tracking-wider text-ink-3 uppercase">
            <HardDrive className="size-3.5" /> Stored at
          </p>
          {(d.destinations ?? []).map((x) => (
            <p key={x} className="mt-2 font-mono text-sm break-all text-ink">
              {x}
            </p>
          ))}
          <p className="mt-1 text-sm text-ink-3">{bytes(d.repoBytes)} on disk, compressed and deduplicated</p>
        </div>
      </section>
      {(d.destinations ?? []).every((x) => x.startsWith("local")) && (
        <p className="mt-3 text-sm text-ink-3">
          Backups live on this box for now. They protect against mistakes, not against losing the box itself; off-box copies come with R2.
        </p>
      )}

      {done && (
        <div className="mt-6 animate-pop rounded-xl border border-rev/40 bg-rev-wash px-5 py-4 text-base text-ink">
          Restored {done.targets?.map((t) => targetCopy[t] ?? t).join(" and ")} from {done.backup} in {ms(done.durationMs)}. If that was a mistake,
          restore <code className="font-mono text-sm">{done.safetyBackup}</code>: it's what was there a moment ago.
        </div>
      )}

      <section className="mt-10" aria-labelledby="list">
        <h2 id="list" className="display-italic mb-3 text-xl text-ink">
          History
        </h2>
        <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
          {list.map((b, k) => (
            <li
              key={b.id}
              className="grid animate-rise grid-cols-[1.25rem_minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 px-4 py-3.5 sm:px-5"
              style={{ animationDelay: `${k * 30}ms` }}
            >
              <span
                className={cn(
                  "size-2 justify-self-center rounded-full",
                  b.status === "ok" ? "bg-rev" : b.status === "failed" ? "bg-irr" : "animate-pulse bg-brass",
                )}
                aria-label={b.status}
              />
              <span className="min-w-0">
                <span className="flex flex-wrap items-center gap-2">
                  <span className="text-base font-medium text-ink" title={full(b.startedAt)}>
                    {relative(b.startedAt)}
                  </span>
                  <span className="rounded-full bg-hover px-2 py-px text-xs text-ink-2">{b.kind}</span>
                  <span className="text-xs text-ink-3">{b.trigger === "pre-restore" ? "safety copy before a restore" : b.trigger}</span>
                </span>
                <span className="mt-0.5 block truncate text-sm text-ink-3">
                  {b.status === "running"
                    ? "Running…"
                    : b.status === "failed"
                      ? b.error
                      : `${bytes(total(b))} · Postgres ${bytes(b.postgres?.sizeBytes)} · Valkey ${bytes(b.valkey?.sizeBytes)} · files ${bytes(Object.values(b.files ?? {}).reduce((n, f) => n + f.sizeBytes, 0))} · ${ms(b.durationMs)}`}
                </span>
              </span>
              {owner && b.status === "ok" && (
                <Button variant="ghost" size="sm" onClick={() => setRestoring(b)}>
                  <RotateCcw />
                  Restore…
                </Button>
              )}
            </li>
          ))}
        </ul>
        {list.length === 0 && <p className="mt-2 text-base text-ink-3">No backups yet.</p>}
      </section>

      <HazardDialog<Preview, BackupRestored>
        key={restoring?.id + targets.join()}
        open={!!restoring}
        onOpenChange={(open) => !open && setRestoring(null)}
        title="Restore this backup?"
        word="restore"
        action="Overwrite with the backup"
        run={(confirm) => mod.restore(restoring!.id, targets, confirm)}
        renderPreview={(p) => (
          <div className="flex flex-col gap-3">
            <fieldset className="flex flex-wrap gap-4 rounded-lg border border-rule px-4 py-3">
              <legend className="px-1 text-sm text-ink-3">Put back</legend>
              {["postgres", "valkey", "files"].map((t) => (
                <label key={t} className="flex items-center gap-2 text-base text-ink">
                  <Checkbox
                    checked={targets.includes(t)}
                    onCheckedChange={(v) => setTargets((x) => (v === true ? [...x, t] : x.filter((y) => y !== t)))}
                  />
                  {targetCopy[t]}
                </label>
              ))}
            </fieldset>
            <div className="rounded-lg border border-irr-rule bg-irr-wash px-4 py-3 text-base text-ink">
              <p className="font-medium">Everything since {relative(p.takenAt)} is lost:</p>
              <ul className="mt-2 flex flex-col gap-2">
                {p.overwrites.map((x) => (
                  <li key={x.target} className="text-ink-2">
                    <span className="font-medium text-ink">{targetCopy[x.target] ?? x.target}:</span> {cap(x.what)}
                    {x.items && x.items.length > 0 && <span className="mt-1 block font-mono text-xs text-ink-3">{x.items.join(" · ")}</span>}
                  </li>
                ))}
              </ul>
            </div>
            <p className="flex gap-2 text-sm text-ink-2">
              <Archive className="mt-0.5 size-4 shrink-0 text-ink-3" />
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

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s).replace(/\d{4}-\d\d-\d\dT[\d:.]+Z/g, (t) => full(t));
