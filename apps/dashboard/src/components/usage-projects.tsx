import { Link } from "@tanstack/react-router";
import { ArrowRight, ChevronRight, Search } from "lucide-react";
import { Accordion as A } from "radix-ui";
import { useDeferredValue, useState } from "react";
import type { BoxResources } from "@/api/client";
import { Empty, Skeleton } from "@/components/page";
import { ProjectIcon } from "@/components/project-icon";
import { DiskBreakdown } from "@/components/usage-disk";
import { cn } from "@/lib/cn";
import { bytes, dec } from "@/lib/format";
import { rowsOf, sharePct, sortRows, type DiskReport, type Row, type SortKey } from "@/lib/footprint";
import { memWords, type Shares } from "@/lib/usage";

const MB = 1048576;
/** Past this many projects the list gets a search box. */
const FIND_FROM = 9;
/** A long list shows this many, then "Show all". */
const FIRST = 20;

/** Project · Memory · Disk · Share of the box: the same four columns however many services a project uses. */
const COLS = "sm:grid-cols-[minmax(0,1fr)_5.5rem_5.5rem_6.5rem]";

/**
 * Every project's footprint: its memory and disk, and its share of the box
 * (the larger of the two), biggest first. A row opens to what's on its disk
 * part by part, its limit and a link to its resources. Fixed width: a new
 * service adds a line inside a row, never a column.
 */
export function UsageProjects({
  names,
  res,
  shares,
  report,
  reportPending,
  held,
  className,
}: {
  names: string[];
  res: BoxResources;
  shares: Shares;
  report?: DiskReport;
  /** The disk report is on its way: the disk column waits for it. */
  reportPending: boolean;
  held: Set<string>;
  className?: string;
}) {
  const [sort, setSort] = useState<SortKey>("footprint");
  const [find, setFind] = useState("");
  const [open, setOpen] = useState<string[]>([]);
  const [all, setAll] = useState(false);
  const needle = useDeferredValue(find.trim().toLowerCase());
  const rows = sortRows(rowsOf(names, res, shares.totalMB, report), sort);
  const hits = needle ? rows.filter((r) => r.name.toLowerCase().includes(needle)) : rows;
  const shown = needle || all ? hits : hits.slice(0, FIRST);

  const head = (key: SortKey, label: string, right = true) => {
    const on = sort === key;
    return (
      <span className={right ? "text-right" : ""}>
        <button
          type="button"
          onClick={() => setSort(key)}
          aria-pressed={on}
          title={key === "name" ? "Sort A to Z" : "Sort biggest first"}
          className={cn("label rounded-[4px] transition-colors duration-[var(--dur-state)] hover:text-ink", on ? "text-ink" : "text-ink-3")}
        >
          {label}
        </button>
      </span>
    );
  };

  return (
    <section className={className} aria-labelledby="by-project">
      <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-2">
        <div>
          <h2 id="by-project" className="text-[0.9375rem] font-[550] text-ink">
            By project
          </h2>
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">{names.length > 1 ? "Biggest first. Open one to see what it keeps on the disk." : "Open it to see what it keeps on the disk."}</p>
        </div>
        {names.length >= FIND_FROM && (
          <label className="flex h-8 w-full items-center gap-2 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)] sm:w-56">
            <Search className="size-3.5 shrink-0 text-ink-3" aria-hidden />
            <input
              value={find}
              onChange={(e) => setFind(e.target.value)}
              placeholder={`Find among ${names.length} projects`}
              aria-label="Find a project"
              spellCheck={false}
              autoComplete="off"
              className="min-w-0 flex-1 bg-transparent text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4"
            />
          </label>
        )}
      </div>

      {names.length === 0 ? (
        <Empty className="mt-4" title="No projects yet.">
          Once a project runs on the box, its memory and disk show here.{" "}
          <Link to="/new" className="text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
            Start one
          </Link>
          .
        </Empty>
      ) : (
        <div className="mt-3">
          <div role="group" aria-label="Sort projects" className={cn("hidden gap-x-4 border-b border-rule pb-2 sm:grid", COLS)}>
            <span className="pl-[3.25rem]">{head("name", "Project", false)}</span>
            {head("memory", "Memory")}
            {head("disk", "Disk")}
            {head("footprint", "Of the box")}
          </div>
          <A.Root type="multiple" value={open} onValueChange={setOpen} asChild>
            <ul aria-label="Each project’s footprint" className="border-rule max-sm:border-t">
              {shown.map((r) => (
                <ProjectRow key={r.name} r={r} open={open.includes(r.name)} held={held.has(r.name)} diskPending={reportPending && !r.disk} />
              ))}
            </ul>
          </A.Root>
          {shown.length < hits.length && (
            <button type="button" onClick={() => setAll(true)} className="mt-2 h-8 rounded-[7px] px-2 text-[0.8125rem] text-ink-2 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover hover:text-ink">
              Show all {hits.length} projects
            </button>
          )}
          {shown.length === 0 && <p className="border-b border-rule py-6 text-[0.875rem] text-ink-3">No project matches “{find.trim()}”.</p>}
        </div>
      )}
    </section>
  );
}

/** "No limit", "Limited to 25% of the box", "Up to 1.4 GB (the box’s default)". */
function limitWords(t: Row["total"]) {
  if (!t || t.limitSource === "automatic") return "No limit";
  if (t.budget.maxSharePercent) return `Limited to ${t.budget.maxSharePercent}% of the box`;
  if (t.limitSource === "box default") return `Up to ${memWords(t.limitBytes / MB)} (the box’s default)`;
  return `Limited to ${memWords(t.limitBytes / MB)}`;
}

function ProjectRow({ r, open, held, diskPending }: { r: Row; open: boolean; held: boolean; diskPending: boolean }) {
  const mem = r.memMB > 0 ? memWords(r.memMB) : "–";
  const disk = r.diskBytes > 0 ? bytes(r.diskBytes) : "–";
  const oom = r.total?.pressure === "oom";
  const trouble = oom ? "ran out of memory" : held ? "read-only: the disk is nearly full" : undefined;
  return (
    <A.Item value={r.name} asChild>
      <li className="border-b border-rule">
        <A.Header asChild>
          <div>
            <A.Trigger
              className={cn(
                "group -mx-2 grid w-[calc(100%+1rem)] cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 rounded-[8px] px-2 py-2.5 text-left transition-colors duration-[var(--dur-state)] hover:bg-paper-hover focus-visible:outline-offset-[-2px]",
                COLS,
              )}
            >
              <span className="flex min-w-0 items-center gap-2.5">
                <ChevronRight aria-hidden className="size-3.5 shrink-0 text-ink-4 transition-transform duration-[var(--dur-state)] group-data-[state=open]:rotate-90 group-hover:text-ink-3" />
                <ProjectIcon project={r.name} size={20} />
                <span className="min-w-0">
                  <span className="block truncate text-[0.875rem] font-[550] text-ink">{r.name}</span>
                  <span className="block truncate text-xs text-ink-3">
                    {/* A phone has no columns: the two numbers go under the name. */}
                    <span className={trouble ? "hidden" : "sm:hidden"}>
                      {r.memMB > 0 && `${mem} memory · `}
                      {diskPending ? "…" : disk} disk
                    </span>
                    <span className="max-sm:hidden">
                      {limitWords(r.total)}
                      {trouble && " · "}
                    </span>
                    {trouble && <span className="text-danger">{trouble}</span>}
                  </span>
                </span>
              </span>
              <span className="text-right text-[0.875rem] text-ink-2 tnum max-sm:hidden">
                <span className="sr-only">Memory </span>
                {mem}
              </span>
              <span className="text-right text-[0.875rem] text-ink-2 tnum max-sm:hidden">
                <span className="sr-only">Disk </span>
                {diskPending ? <Skeleton className="ml-auto h-4 w-12" /> : disk}
              </span>
              <ShareMark row={r} />
            </A.Trigger>
          </div>
        </A.Header>
        <A.Content className="pt-1 pb-4 sm:pl-[3.25rem]">{open && <Detail r={r} />}</A.Content>
      </li>
    </A.Item>
  );
}

/** The share of the box as a slim mark and a number: the larger of its memory and disk shares. */
function ShareMark({ row: r }: { row: Row }) {
  const w = Math.min(100, r.share * 100);
  const what = r.diskShare > r.memShare ? "disk" : "memory";
  return (
    <span className="flex items-center justify-end gap-2.5" title={`${sharePct(r.memShare)} of the memory, ${sharePct(r.diskShare)} of the disk`}>
      <span aria-hidden className="relative h-[3px] w-10 overflow-hidden rounded-full bg-rule-2">
        <span className={cn("absolute inset-y-0 left-0 rounded-full bg-ink-3", r.share > 0 && "min-w-[2px]")} style={{ width: `${w}%` }} />
      </span>
      <span className="w-8 text-right text-[0.8125rem] text-ink-2 tnum">
        {r.share > 0 ? sharePct(r.share) : "–"}
        <span className="sr-only"> of the box’s {what}</span>
      </span>
    </span>
  );
}

function Detail({ r }: { r: Row }) {
  const cpu = r.cpuPercent ?? 0;
  return (
    <div>
      <p className="flex flex-wrap gap-x-5 gap-y-1 text-[0.8125rem]">
        <Fact label="CPU" value={cpu < 1 ? "idle" : `${dec(cpu, 0)}% of one`} />
        <Fact label="Of the box" value={`${sharePct(r.memShare)} of the memory, ${sharePct(r.diskShare)} of the disk`} />
        {/* A phone's row shows the numbers instead of the limit. */}
        <span className="sm:hidden">
          <Fact label="Limit" value={limitWords(r.total)} />
        </span>
      </p>
      {r.disk ? (
        <DiskBreakdown disk={r.disk} className="mt-3" />
      ) : (
        <p className="mt-3 text-[0.8125rem] text-ink-3">
          {r.diskBytes > 0 ? `Its databases and files take ${bytes(r.diskBytes)}. ` : ""}A box updated to the latest Tiffin breaks the disk down by part.
        </p>
      )}
      <Link
        to="/projects/$project/usage"
        params={{ project: r.name }}
        className="mt-3 inline-flex items-center gap-1 text-[0.8125rem] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink hover:decoration-ink-3"
      >
        {r.name}’s resources and limits
        <ArrowRight aria-hidden className="size-3.5" />
      </Link>
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <span className="whitespace-nowrap">
      <span className="text-ink-3">{label}</span> <span className="text-ink tnum">{value}</span>
    </span>
  );
}
