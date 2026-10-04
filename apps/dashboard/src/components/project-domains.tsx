import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Globe } from "lucide-react";
import { cn } from "@/lib/cn";
import { projectDomainsQuery, stateWords, withWww, type Tone } from "@/lib/domains";

/** A domain's state as a small mark: a green dot when live, a spinner while its certificate comes, a ring while DNS catches up, red for a problem. */
export function StatusDot({ tone, className }: { tone: Tone; className?: string }) {
  if (tone === "busy")
    return (
      <span className={cn("inline-grid size-2 shrink-0 place-items-center", className)} aria-hidden>
        <span className="spinner size-2.5! text-brass-ink" />
      </span>
    );
  return (
    <span
      aria-hidden
      className={cn(
        "inline-block size-2 shrink-0 rounded-full",
        tone === "ok" ? "bg-ok" : tone === "bad" ? "bg-danger" : "shadow-[inset_0_0_0_1.5px_var(--ink-4)]",
        className,
      )}
    />
  );
}

/**
 * "Domains" on a project's overview: each of its own domains with its state
 * in words, or one quiet line offering one. Opens Settings › Domains.
 */
export function DomainsSummary({ project, className }: { project: string; className?: string }) {
  const q = useQuery(projectDomainsQuery(project));
  if (q.isError || !q.data) return null;
  const rows = withWww(q.data);
  const to = { to: "/projects/$project/domains" as const, params: { project } };
  return (
    <section className={className} aria-labelledby="domains-h">
      <div className="mb-2.5 flex items-baseline justify-between gap-4">
        <h2 id="domains-h" className="label">
          Domains
        </h2>
        {rows.length > 0 && (
          <Link {...to} className="text-[0.8125rem] text-ink-3 hover:text-ink">
            Manage
          </Link>
        )}
      </div>
      {rows.length === 0 ? (
        <Link {...to} className="group flex items-center gap-3 border-y border-rule py-3 text-[0.875rem]">
          <Globe className="size-4 shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1">
            <span className="text-ink group-hover:underline group-hover:decoration-rule-3 group-hover:underline-offset-4">Use your own domain</span>
            <span className="text-ink-3"> · like {project}.com, with HTTPS</span>
          </span>
          <ChevronRight className="size-4 shrink-0 text-ink-4" />
        </Link>
      ) : (
        <ul className="divide-y divide-rule border-y border-rule">
          {rows.map(({ d, www }) => {
            const s = stateWords(d);
            const w = www && www.state !== "live" && d.state === "live" ? stateWords(www) : null;
            return (
              <li key={d.domain}>
                <Link {...to} className="group grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 py-2.5">
                  <StatusDot tone={s.tone} />
                  <span className="min-w-0 truncate">
                    <span className="ident text-[0.8125rem] text-ink group-hover:underline group-hover:decoration-rule-3 group-hover:underline-offset-4">{d.domain}</span>
                  </span>
                  <span className={cn("text-[0.8125rem]", s.tone === "bad" ? "text-danger" : s.tone === "ok" ? "text-ink-3" : "text-ink-2")}>
                    {w ? `www: ${w.word.toLowerCase()}` : s.tone === "bad" ? "Problem" : s.word}
                  </span>
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/** One line in a project's Settings: how many domains, and the way to them. */
export function DomainsLink({ project }: { project: string }) {
  const q = useQuery(projectDomainsQuery(project));
  if (q.isError) return null;
  const rows = q.data ? withWww(q.data) : undefined;
  const waiting = rows?.filter((r) => r.d.state !== "live").length ?? 0;
  return (
    <Link to="/projects/$project/domains" params={{ project }} className="group mt-3 flex items-center justify-between gap-3 border-y border-rule py-2.5">
      <span className="min-w-0">
        <span className="block text-[0.875rem] text-ink group-hover:underline group-hover:decoration-rule-3 group-hover:underline-offset-4">
          {!rows ? "Domains" : rows.length === 0 ? "Use your own domain" : rows.length === 1 ? `Your domain: ${rows[0].d.domain}` : `${rows.length} domains of your own`}
        </span>
        <span className="block text-xs text-ink-3">{waiting ? `${waiting === 1 ? "One is" : `${waiting} are`} still being set up.` : "Like example.com, with HTTPS that renews itself."}</span>
      </span>
      <ChevronRight className="size-4 shrink-0 text-ink-4" />
    </Link>
  );
}
