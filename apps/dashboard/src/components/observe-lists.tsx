import { Link } from "@tanstack/react-router";
import type { Issue, TraceSummary } from "@/api/modules";
import { LevelWord } from "@/components/health-kit";
import { cn } from "@/lib/cn";
import { countWords, ms, num } from "@/lib/format";
import { full, relative } from "@/lib/time";

// Rows for one project's errors and requests, as the box-wide Errors and
// Requests pages draw them, minus the project (the page already says which).

/** "TypeError: Cannot read properties…" → the type and the message, so the type can be quieter. */
function splitTitle(t: string): { type?: string; message: string } {
  const m = t.match(/^([A-Z][\w.]*(?:Error|Exception|Warning|Panic)?):\s+(.*)$/);
  return m && m[1].length < 40 ? { type: m[1], message: m[2] } : { message: t };
}

/** Error issues, newest trouble as the API orders them; each opens its issue page. */
export function IssueRows({ issues, resolved, compact }: { issues: Issue[]; resolved?: boolean; compact?: boolean }) {
  return (
    <ul className="divide-y divide-rule border-y border-rule">
      {issues.map((i) => {
        const t = splitTitle(i.title);
        return (
          <li key={i.id}>
            <Link
              to="/errors/$id"
              params={{ id: i.id }}
              className={cn(
                "group -mx-2 grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-6 rounded-[6px] px-2 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover",
                compact ? "py-2.5" : "py-3.5 sm:grid-cols-[3.25rem_minmax(0,1fr)_9.5rem]",
              )}
            >
              {!compact && <LevelWord level={i.level} className="hidden pt-px text-[0.8125rem] sm:block" />}
              <span className="min-w-0">
                <span className={cn("block text-ink [overflow-wrap:anywhere]", compact ? "line-clamp-2 text-[0.875rem] leading-5" : "text-[0.9375rem] leading-[1.375rem]")}>
                  {t.type && <span className="font-[550]">{t.type}: </span>}
                  {t.message}
                </span>
                <span className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-[0.8125rem] text-ink-3">
                  <span className="text-ink-2">{i.app}</span>
                  {!compact && i.culprit && <span className="ident text-ink-2">{i.culprit}</span>}
                  {!compact && i.lastRelease && <span className="ident">{i.lastRelease}</span>}
                  <LevelWord level={i.level} className={compact ? undefined : "sm:hidden"} />
                </span>
              </span>
              <span className="text-right">
                <span className={cn("block text-ink tnum", compact ? "text-[0.9375rem] leading-5" : "text-[1.0625rem] leading-6")}>{num(i.count)}</span>
                <span className="block text-xs text-ink-3">{resolved && i.resolvedAt ? `resolved ${relative(i.resolvedAt)}` : `last seen ${relative(i.lastSeen)}`}</span>
              </span>
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

/** Why the box kept a trace, as a person would say it. */
const keptWords: Record<TraceSummary["kept"], string> = { error: "failed", slow: "slow", sampled: "sample" };

function Status({ t }: { t: Pick<TraceSummary, "status" | "error"> }) {
  if (!t.status) return t.error ? <span className="text-danger">failed</span> : null;
  return <span className={cn("tnum", t.status >= 500 || t.error ? "text-danger" : t.status >= 400 ? "text-warn-ink" : "text-ink-3")}>{t.status}</span>;
}

/** Traced requests; each opens its timeline of steps. */
export function TraceRows({ traces, compact }: { traces: TraceSummary[]; compact?: boolean }) {
  return (
    <ul className="divide-y divide-rule border-y border-rule">
      {traces.map((t) => (
        <li key={t.traceId}>
          <Link
            to="/requests/$project/$id"
            params={{ project: t.project, id: t.traceId }}
            className={cn(
              "-mx-2 grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-6 rounded-[6px] px-2 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover",
              compact ? "py-2.5" : "py-3 sm:grid-cols-[minmax(0,1fr)_6rem_5.5rem]",
            )}
          >
            <span className="min-w-0">
              <span className="block font-mono text-[0.84375rem] leading-[1.375rem] text-ink [overflow-wrap:anywhere]">{t.name}</span>
              <span className="mt-0.5 flex flex-wrap items-center gap-x-2.5 text-[0.8125rem] text-ink-3">
                <span className="text-ink-2">{t.app}</span>
                <span title={full(t.start)}>{relative(t.start)}</span>
                {!compact && <span>{countWords(t.spans, "step")}</span>}
                <span className={t.kept === "error" ? "text-danger" : undefined}>{keptWords[t.kept]}</span>
                {compact && !!t.status && <Status t={t} />}
              </span>
            </span>
            {!compact && (
              <span className="hidden pt-0.5 text-right text-[0.8125rem] sm:block">
                <Status t={t} />
              </span>
            )}
            <span className={cn("pt-0.5 text-right text-[0.9375rem] tnum", t.durationMs >= 1000 ? "text-ink" : "text-ink-2")}>{ms(t.durationMs)}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}
