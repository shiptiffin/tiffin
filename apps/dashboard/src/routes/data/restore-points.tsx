import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { GitBranch, RotateCcw } from "lucide-react";
import { Fragment, useState } from "react";
import { mod, type PgSnapshot } from "@/api/modules";
import { HazardDialog } from "@/components/hazard";
import { Skeleton } from "@/components/page";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { bytes, count, duration } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, dayKey, dayLabel, full, relative } from "@/lib/time";

const WEEK = 7 * 24 * 3600_000;

/** Why a restore point was taken, as a sentence start: "SQL that changed data". */
export const reasonWords = (r: string) => {
  const s = r.replace(/^an SQL write$/, "SQL that changed data");
  return s.charAt(0).toUpperCase() + s.slice(1);
};

/** "in 5 d", "in 3 h": when a restore point goes. */
const goesIn = (iso: string, now: number) => {
  const s = Math.max(60, Math.round((new Date(iso).getTime() - now) / 1000));
  // Whole days, or whole hours under two days: "in 5 days", "in 18 h".
  return `in ${duration(s >= 2 * 86400 ? Math.round(s / 86400) * 86400 : s >= 3600 ? Math.round(s / 3600) * 3600 : s)}`;
};

/**
 * Restore points: the database as it was before something risky, kept for
 * 7 days. A week strip shows when they were taken; the list, by day, says
 * why, and Restore goes back (after taking one of now).
 */
export function RestorePoints({ project, list, loaded }: { project: string; list: PgSnapshot[]; loaded: boolean }) {
  const qc = useQueryClient();
  const { can, admin } = useMe();
  const [restoring, setRestoring] = useState<PgSnapshot | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [all, setAll] = useState(false);
  const [now] = useState(() => Date.now());
  const shown = all ? list : list.slice(0, 20);
  const oldest = list[list.length - 1];
  const writer = can("apply:irreversible");

  return (
    <section aria-labelledby="snaps" className="max-w-[60rem]">
      <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-1">
        <h2 id="snaps" className="text-md font-[550] text-ink">
          {loaded ? (list.length ? count(list.length, "restore point") : "No restore points yet") : "Restore points"}
        </h2>
        {list.length > 0 && (
          <p className="text-sm text-ink-3">
            Newest {relative(list[0].at)}
            {oldest && <> · the oldest goes {goesIn(oldest.expiresAt, now)}</>}
          </p>
        )}
      </div>
      <p className="mt-1.5 max-w-[44rem] text-base text-ink-2">
        Tiffin saves the whole database before anything that can lose data: SQL that changes data, deleting many rows or a table, and a restore. Each is kept for 7
        days.
        {admin && (
          <>
            {" "}
            For older days, the box keeps{" "}
            <Link to="/backups" className="text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
              backups
            </Link>
            .
          </>
        )}
      </p>

      {!loaded ? (
        <Skeleton className="mt-6 h-64" />
      ) : (
        <>
          <WeekStrip list={list} now={now} />
          {done && (
            <p role="status" className="mt-5 rounded-md border border-rule-2 bg-paper-raised px-3 py-2 text-base text-ink">
              {done}
            </p>
          )}
          {list.length === 0 ? (
            <p className="mt-6 rounded-[10px] border border-dashed border-rule-3 px-6 py-8 text-center text-base text-ink-3">
              Nothing risky has happened in the last 7 days. The next one is taken on its own.
            </p>
          ) : (
            <div className="mt-6">
              {shown.map((s, i) => {
                const day = dayKey(s.at);
                const first = i === 0 || dayKey(shown[i - 1].at) !== day;
                return (
                  <Fragment key={s.id}>
                    {first && <h3 className={cn("label border-b border-rule pb-2", i > 0 && "mt-6")}>{dayLabel(s.at)}</h3>}
                    <div className="group grid grid-cols-[3rem_minmax(0,1fr)_auto] items-center gap-x-3 border-b border-rule py-2 sm:grid-cols-[3.5rem_minmax(0,1fr)_5rem_5.5rem_7rem] sm:gap-x-4">
                      <time dateTime={s.at} title={full(s.at)} className="text-sm text-ink-3 tnum">
                        {clock(s.at)}
                      </time>
                      <span className="min-w-0">
                        <span className="block truncate text-base text-ink">{reasonWords(s.reason)}</span>
                        {s.branch && (
                          <span className="mt-0.5 inline-flex items-center gap-1 text-sm text-ink-3">
                            <GitBranch className="size-3" aria-hidden />
                            on the copy <span className="font-mono text-ink-2">{s.branch}</span>
                          </span>
                        )}
                        <span className="block text-xs text-ink-3 sm:hidden">
                          {bytes(s.sizeBytes)} · goes {goesIn(s.expiresAt, now)}
                        </span>
                      </span>
                      <span className="hidden text-right text-sm text-ink-2 tnum sm:block">{bytes(s.sizeBytes)}</span>
                      <span className="hidden text-right text-sm text-ink-3 tnum sm:block" title={`Kept until ${full(s.expiresAt)}`}>
                        goes {goesIn(s.expiresAt, now)}
                      </span>
                      <span className="flex justify-end">
                        {writer && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setRestoring(s)}
                            aria-label={`Restore the database to ${full(s.at)}`}
                            className="max-sm:w-8 max-sm:px-0 sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100"
                          >
                            <RotateCcw />
                            <span className="max-sm:sr-only">Restore</span>
                          </Button>
                        )}
                      </span>
                    </div>
                  </Fragment>
                );
              })}
            </div>
          )}
          {list.length > shown.length && (
            <button type="button" onClick={() => setAll(true)} className="mt-3 text-sm text-ink-3 hover:text-ink">
              Show {list.length - shown.length} more
            </button>
          )}
          {!writer && list.length > 0 && <p className="mt-3 text-sm text-ink-3">Going back needs full access to the project.</p>}
        </>
      )}

      <HazardDialog<{ overwrites: string; takenAt: string; database: string }, { restored: string; before?: string }>
        open={!!restoring}
        onOpenChange={(o) => !o && setRestoring(null)}
        title="Go back to this restore point?"
        word={project}
        action="Replace the database"
        run={(confirm) => mod.restoreSnapshot(project, restoring!.id, confirm)}
        renderPreview={(p) => (
          <div className="rounded-lg border border-danger-rule bg-danger-wash px-4 py-3 text-base text-ink">
            <p>
              Everything in {restoring?.branch ? <>the copy <span className="font-mono">{restoring.branch}</span></> : "the database"} is replaced by how it was{" "}
              {relative(p.takenAt)}, before {reasonWords(restoring?.reason ?? "").replace(/^./, (c) => c.toLowerCase())}.
            </p>
            <p className="mt-2 text-sm text-ink-2">A restore point of what's there now is taken first, so you can come back.</p>
          </div>
        )}
        onDone={() => {
          setDone("Restored. What was there before is now the newest restore point.");
          for (const k of ["snapshots", "tables", "pg-rows", "pg-table", "pg"]) void qc.invalidateQueries({ queryKey: [k, project] });
        }}
      />
    </section>
  );
}

/** The last 7 days as a strip, midnight to midnight, a tick for each restore point; today on the right. */
function WeekStrip({ list, now }: { list: PgSnapshot[]; now: number }) {
  const today = new Date(now);
  today.setHours(0, 0, 0, 0);
  const start = today.getTime() - 6 * 24 * 3600_000;
  const at = (t: number) => (t - start) / WEEK;
  const days = Array.from({ length: 7 }, (_, i) => {
    const d = new Date(start + i * 24 * 3600_000 + 12 * 3600_000);
    return { key: i, label: i === 6 ? "Today" : new Intl.DateTimeFormat(undefined, { weekday: "short" }).format(d) };
  });
  const nowX = at(now);
  return (
    <div className="mt-6" role="img" aria-label={`${count(list.length, "restore point")} over the last 7 days`}>
      <div className="relative h-9 overflow-hidden rounded-md border border-rule-2 bg-paper-raised">
        {/* The rest of today hasn't happened yet. */}
        <span aria-hidden className="absolute inset-y-0 right-0 bg-paper-sunk" style={{ left: `${nowX * 100}%` }} />
        {days.slice(1).map((d) => (
          <span key={d.key} aria-hidden className="absolute inset-y-0 w-px bg-rule" style={{ left: `${(d.key / 7) * 100}%` }} />
        ))}
        {list.map((s) => {
          const x = at(new Date(s.at).getTime());
          if (x < 0 || x > 1) return null;
          return (
            <span
              key={s.id}
              title={`${reasonWords(s.reason)}, ${full(s.at)}`}
              className={cn("absolute top-2 bottom-2 w-[3px] -translate-x-1/2 rounded-full", s.branch ? "bg-ink-4" : "bg-ink-2")}
              style={{ left: `max(4px, ${x * 100}%)` }}
            />
          );
        })}
      </div>
      <div className="mt-1.5 grid grid-cols-7 text-xs text-ink-3">
        {days.map((d) => (
          <span key={d.key} className={cn("text-center", d.key === 6 && "text-ink-2")}>
            {d.label}
          </span>
        ))}
      </div>
    </div>
  );
}
