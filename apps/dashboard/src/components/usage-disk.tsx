import { useQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { Collapsible } from "radix-ui";
import { useState } from "react";
import { cn } from "@/lib/cn";
import { bytes, count } from "@/lib/format";
import { boxDiskQuery, partsOf, sharePct, type DiskReport, type ProjectDisk } from "@/lib/footprint";

/**
 * What a project keeps on the data disk, part by part, biggest first: its
 * images, database, files, KV, logs, build files and snapshots. Parts it
 * doesn't have are named in one line under the list, so the list is as long
 * as what the project uses, not as long as what Tiffin offers.
 */
export function DiskBreakdown({ disk, className }: { disk: ProjectDisk; className?: string }) {
  const parts = partsOf(disk);
  const none = ALL.filter((k) => !parts.some((p) => p.key === k[0])).map((k) => k[1]);
  return (
    <div className={className}>
      {parts.length === 0 ? (
        <p className="text-[0.8125rem] text-ink-3">Nothing on the disk yet.</p>
      ) : (
        <ul className="grid gap-x-8 sm:grid-cols-2">
          {parts.map((p) => (
            <li key={p.key} className="flex items-baseline justify-between gap-4 border-b border-rule py-2">
              <span className="min-w-0">
                <span className="block text-[0.8125rem] text-ink">{p.name}</span>
                <span className="block truncate text-xs text-ink-3" title={p.note}>
                  {p.note}
                </span>
              </span>
              <span className="shrink-0 text-[0.8125rem] text-ink-2 tnum">{bytes(p.bytes)}</span>
            </li>
          ))}
        </ul>
      )}
      {parts.length > 0 && none.length > 0 && <p className="mt-2 text-xs text-ink-3">No {list(none)}.</p>}
    </div>
  );
}

const ALL: Array<[string, string]> = [
  ["images", "images"],
  ["db", "database"],
  ["files", "files"],
  ["kv", "KV"],
  ["logs", "logs"],
  ["build", "build files"],
  ["snapshots", "snapshots"],
];

/** ["a", "b", "c"] → "a, b or c". */
const list = (xs: string[]) => (xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")} or ${xs[xs.length - 1]}`);

/**
 * What the box clears by itself: the build cache against its cap, images
 * nothing needs any more, and what deleted projects left behind. Each line
 * says when it goes, so a full-looking disk isn't a surprise.
 */
export function DiskSweep({ report, className }: { report: DiskReport; className?: string }) {
  const left = (report.projects ?? []).filter((p) => !p.exists && p.totalBytes > 0);
  const cache = report.buildCacheBytes ?? 0;
  const cap = report.buildCacheCapBytes ?? 0;
  return (
    <section className={className} aria-labelledby="sweep">
      <h2 id="sweep" className="text-[0.9375rem] font-[550] text-ink">
        Cleared by the box
      </h2>
      <p className="mt-0.5 text-[0.8125rem] text-ink-3">Every hour the box removes what nothing needs. You don’t have to do anything.</p>
      <ul className="mt-3 divide-y divide-rule border-y border-rule">
        {cache > 0 && (
          <Line
            name="Build cache"
            note={
              cap > 0
                ? cache > cap
                  ? `Over its ${bytes(cap, 0)} cap: the next hourly sweep trims the oldest steps.`
                  : `Makes rebuilds faster. Kept under ${bytes(cap, 0)}, oldest steps first.`
                : "Makes rebuilds faster. Its oldest steps are trimmed each hour."
            }
            value={bytes(cache)}
          />
        )}
        <Line
          name="Unused images"
          note={report.unusedImages > 0 ? "Old builds nothing rolls back to. Removed hourly once they’re 6 hours old." : "None waiting. Old builds are removed hourly once they’re 6 hours old."}
          value={report.unusedImages > 0 ? `${count(report.unusedImages, "image")} · ${bytes(report.unusedImageBytes)}` : "–"}
          muted={report.unusedImages === 0}
        />
        {left.length > 0 && <Leftovers left={left} />}
      </ul>
    </section>
  );
}

function Line({ name, note, value, muted }: { name: string; note: string; value: string; muted?: boolean }) {
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-6 py-2.5">
      <span className="min-w-0">
        <span className="block text-[0.875rem] text-ink">{name}</span>
        <span className="block text-xs text-ink-3">{note}</span>
      </span>
      <span className={cn("text-[0.875rem] whitespace-nowrap tnum", muted ? "text-ink-4" : "text-ink-2")}>{value}</span>
    </li>
  );
}

const LEFT_FIRST = 10;

/** Deleted projects' leftovers, greyed: one line, opening to each project and when its part goes. */
function Leftovers({ left }: { left: ProjectDisk[] }) {
  const [open, setOpen] = useState(false);
  const [more, setMore] = useState(false);
  const total = left.reduce((t, p) => t + p.totalBytes, 0);
  const onlySnaps = (p: ProjectDisk) => p.backupBytes === p.totalBytes;
  const allSnaps = left.every(onlySnaps);
  const anySnaps = left.some((p) => p.backupBytes > 0);
  const note = allSnaps
    ? "Database snapshots taken as they were deleted, kept up to 7 days."
    : anySnaps
      ? "Removed within the hour. Database snapshots are kept up to 7 days."
      : "Removed within the hour.";
  return (
    <li>
      <Collapsible.Root open={open} onOpenChange={setOpen}>
        <Collapsible.Trigger className="group grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-6 py-2.5 text-left">
          <span className="min-w-0">
            <span className="flex items-center gap-1 text-[0.875rem] text-ink">
              Deleted projects
              <ChevronRight aria-hidden className={cn("size-3.5 text-ink-3 transition-transform duration-[var(--dur-state)]", open && "rotate-90")} />
            </span>
            <span className="block text-xs text-ink-3">{note}</span>
          </span>
          <span className="text-[0.875rem] whitespace-nowrap text-ink-2 tnum">
            {count(left.length, "project")} · {bytes(total)}
          </span>
        </Collapsible.Trigger>
        <Collapsible.Content>
          <ul className="mb-2.5 grid gap-x-8 sm:grid-cols-2" aria-label="What deleted projects left">
            {(more ? left : left.slice(0, LEFT_FIRST)).map((p) => (
              <li key={p.project} className="flex items-baseline justify-between gap-4 border-t border-rule py-1.5 text-[0.8125rem] text-ink-4">
                <span className="min-w-0 truncate">
                  {p.project}
                  {!allSnaps && <span className="ml-2 text-xs">{onlySnaps(p) ? "snapshot, up to 7 days" : "within the hour"}</span>}
                </span>
                <span className="shrink-0 tnum">{bytes(p.totalBytes)}</span>
              </li>
            ))}
          </ul>
          {!more && left.length > LEFT_FIRST && (
            <button type="button" onClick={() => setMore(true)} className="mb-2.5 h-7 rounded-[6px] px-1.5 text-[0.8125rem] text-ink-3 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover hover:text-ink">
              {left.length - LEFT_FIRST} more
            </button>
          )}
        </Collapsible.Content>
      </Collapsible.Root>
    </li>
  );
}

/**
 * A project's own row of the disk report, for its resources page: everything
 * it keeps on the data disk, not just the databases and files its storage
 * limit counts. Hidden when the box can't say (an older box, or a token
 * that can't read the whole box).
 */
export function ProjectFootprint({ project, className }: { project: string; className?: string }) {
  const report = useQuery(boxDiskQuery);
  const d = report.data?.projects?.find((p) => p.project === project && p.exists);
  if (!d || !report.data) return null;
  const share = report.data.disk.totalBytes > 0 ? d.totalBytes / report.data.disk.totalBytes : 0;
  return (
    <section className={className} aria-labelledby="on-disk">
      <div className="mb-1.5 flex items-baseline justify-between gap-4">
        <h2 id="on-disk" className="label">
          On disk
        </h2>
        <span className="text-[0.8125rem] text-ink-2 tnum">
          {bytes(d.totalBytes)} <span className="text-ink-3">· {sharePct(share)} of the disk</span>
        </span>
      </div>
      <DiskBreakdown disk={d} className="border-t border-rule" />
      <p className="mt-2 text-xs text-ink-3">Its storage limit counts its databases and files. Builds older than its rollback targets are cleared by the box each hour.</p>
    </section>
  );
}
