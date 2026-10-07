import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { notOnBox } from "@/api/client";
import { Segmented } from "@/components/health-kit";
import { useTopPaths, type PathRow, type Range } from "@/components/observe-data";
import { Skeleton } from "@/components/page";
import { cn } from "@/lib/cn";
import { int, ms, pct } from "@/lib/format";

type Sort = "busiest" | "slowest" | "failing";
const SHOWN = 8;

/**
 * Routes: the paths people asked for in the window, from the box's edge log
 * (every request, not a sample): how many, how many failed, how fast. Ids in
 * a path are folded, so /orders/:id is one row. Each opens its requests in
 * the logs.
 */
export function TopPaths({ project, range, app, enabled }: { project: string; range: Range; app?: string; enabled: boolean }) {
  const q = useTopPaths(project, range, app, enabled);
  const [sort, setSort] = useState<Sort>("busiest");
  const [all, setAll] = useState(false);
  const rows = [...(q.data ?? [])].sort((a, b) =>
    sort === "slowest" ? b.p95 - a.p95 : sort === "failing" ? b.failed - a.failed || b.failed / (b.requests || 1) - a.failed / (a.requests || 1) : b.requests - a.requests,
  );
  const list = sort === "failing" ? rows.filter((r) => r.failed > 0) : rows;
  const shown = all ? list.slice(0, 50) : list.slice(0, SHOWN);
  const cols = "sm:grid-cols-[minmax(0,1fr)_6.5rem_5.5rem_6rem_6rem]";

  return (
    <section className="mt-12" aria-labelledby="routes">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-3">
        <div>
          <h2 id="routes" className="text-[0.9375rem] font-[550] text-ink">
            Routes
          </h2>
          <p className="text-[0.8125rem] text-ink-3">Every request the box answered, grouped by path. Ids in a path are folded into one route.</p>
        </div>
        {(q.data?.length ?? 0) > 1 && (
          <Segmented
            label="Sort routes"
            value={sort}
            onChange={(v) => (setSort(v), setAll(false))}
            options={[
              { v: "busiest", label: "Busiest" },
              { v: "slowest", label: "Slowest" },
              { v: "failing", label: "Failing" },
            ]}
          />
        )}
      </div>

      <div className="mt-4">
        {q.isPending && q.fetchStatus !== "idle" ? (
          <Skeleton className="h-48" />
        ) : q.isError || !enabled ? (
          <Quiet>{q.isError && notOnBox(q.error) ? "Routes are counted on a running box." : "Routes show here once the box’s log store is answering."}</Quiet>
        ) : list.length === 0 ? (
          <Quiet>{sort === "failing" ? "No route answered with a 5xx in this time." : "No requests in this time. Routes show here a few seconds after the first visit."}</Quiet>
        ) : (
          <>
            <div className={cn("hidden gap-x-4 pb-1.5 text-xs text-ink-3 sm:grid", cols)} aria-hidden>
              <span>Path</span>
              <span className="text-right">Requests</span>
              <span className="text-right">Failed</span>
              <span className="text-right">Half within</span>
              <span className="text-right">95% within</span>
            </div>
            <ul className="divide-y divide-rule border-y border-rule">
              {shown.map((r) => (
                <PathLine key={r.path} r={r} project={project} range={range} cols={cols} max={rows[0]?.requests ?? 1} />
              ))}
            </ul>
            {list.length > SHOWN && (
              <button type="button" onClick={() => setAll((x) => !x)} className="mt-2 text-[0.8125rem] text-ink-3 underline-offset-4 hover:text-ink hover:underline">
                {all ? "Show fewer" : `Show ${int(Math.min(50, list.length) - SHOWN)} more`}
              </button>
            )}
          </>
        )}
      </div>
    </section>
  );
}

function PathLine({ r, project, range, cols, max }: { r: PathRow; project: string; range: Range; cols: string; max: number }) {
  const rate = r.requests ? r.failed / r.requests : 0;
  const fail = r.failed ? <span className={rate >= 0.01 ? "text-danger" : "text-ink-2"}>{pct(rate, rate < 0.001 ? 2 : 1)}</span> : <span className="text-ink-4">none</span>;
  // The route's own requests open in the logs; a folded route searches its prefix.
  const prefix = r.path.includes(":id") ? r.path.slice(0, r.path.indexOf(":id")) : r.path;
  const query = `source:edge path:${JSON.stringify(prefix)}${r.path.includes(":id") ? "*" : ""}`;
  return (
    <li>
      <Link
        to="/projects/$project/logs"
        params={{ project }}
        search={{ q: query, since: range }}
        className={cn("group relative -mx-2 grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 rounded-[6px] px-2 py-2.5 text-[0.875rem] transition-colors duration-[var(--dur-state)] hover:bg-paper-hover", cols)}
      >
        <span className="relative min-w-0">
          <span className="block truncate font-mono text-[0.8125rem] text-ink" title={r.paths > 1 ? `${r.path}: ${int(r.paths)} addresses` : r.path}>
            {r.path}
          </span>
          {/* The route's share of the busiest one: the list reads as a ranking at a glance. */}
          <span aria-hidden className="mt-1 block h-[3px] rounded-full bg-paper-sunk">
            <span className="block h-full rounded-full bg-ink-4" style={{ width: `${Math.max(1, (r.requests / max) * 100)}%` }} />
          </span>
          <span className="block text-xs text-ink-3 sm:hidden">
            {fail} failed · half within {ms(r.p50)} · 95% within {ms(r.p95)}
          </span>
        </span>
        <span className="relative text-right text-ink tnum">{int(r.requests)}</span>
        <span className="relative hidden text-right tnum sm:block">{fail}</span>
        <span className="relative hidden text-right text-ink-2 tnum sm:block">{ms(r.p50)}</span>
        <span className={cn("relative hidden text-right tnum sm:block", r.p95 >= 1000 ? "text-warn-ink" : "text-ink-2")}>{ms(r.p95)}</span>
      </Link>
    </li>
  );
}

function Quiet({ children }: { children: React.ReactNode }) {
  return <p className="rounded-[10px] border border-dashed border-rule-2 px-5 py-7 text-center text-[0.875rem] text-ink-3">{children}</p>;
}
