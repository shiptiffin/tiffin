import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";
import { useEnamel } from "@/lib/enamel";
import { EnamelSwatch } from "./enamel-swatch";

/**
 * One frame for every page, so nothing jumps sideways between pages: every
 * page starts at the same left edge (48 px from the sidebar on a desktop,
 * 16 px on a phone). Width only limits how far right a page reaches:
 *   default  52 rem   reading pages, forms, settings
 *   wide     72 rem   lists and tables
 *   full     the whole frame (the Box, logs)
 */
export function Page({ children, className, wide, full }: { children: ReactNode; className?: string; wide?: boolean; full?: boolean }) {
  return (
    <div className="w-full max-w-[84rem] px-4 pt-8 pb-24 sm:px-8 sm:pt-10 lg:px-12">
      <div className={cn("w-full", full ? "" : wide ? "max-w-[72rem]" : "max-w-[52rem]", className)}>{children}</div>
    </div>
  );
}

/** "shop › Storage": where a page sits. The last part is the page itself, so it isn't repeated. */
export function Crumbs({ items }: { items: Array<{ label: ReactNode; to?: string; params?: Record<string, string>; mono?: boolean }> }) {
  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-[0.8125rem] text-ink-3">
      {items.map((c, i) => (
        <span key={i} className="flex min-w-0 items-center gap-1.5">
          {i > 0 && (
            <span aria-hidden className="text-ink-4">
              ›
            </span>
          )}
          {c.to ? (
            <Link to={c.to as "/"} params={c.params as never} className="truncate transition-colors hover:text-ink">
              {c.to === "/projects/$project" && typeof c.label === "string" && c.params?.project ? <ProjectLabel project={c.params.project} /> : c.label}
            </Link>
          ) : (
            <span className="truncate">{c.label}</span>
          )}
        </span>
      ))}
    </nav>
  );
}

/** A project in the breadcrumb wears its enamel, as in the sidebar. */
function ProjectLabel({ project }: { project: string }) {
  const enamel = useEnamel(project);
  return (
    <span className="inline-flex items-center gap-1.5">
      <EnamelSwatch enamel={enamel} size={7} />
      {project}
    </span>
  );
}

/** Crumbs or a small line above, the title (a noun, in Instrument Sans), one line of why, actions on the right. */
export function PageHeader({
  eyebrow,
  title,
  lede,
  actions,
  children,
}: {
  eyebrow?: ReactNode;
  title: ReactNode;
  lede?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <header>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          {eyebrow && <div className="mb-2 text-[0.8125rem] text-ink-3">{eyebrow}</div>}
          <h1 className="title text-ink">{title}</h1>
          {lede && <p className="mt-1.5 max-w-[40rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">{lede}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap gap-2 self-start sm:self-auto">{actions}</div>}
      </div>
      {children}
    </header>
  );
}

/** Link tabs under a page header. */
export function Tabs({ items }: { items: Array<{ to: string; params?: Record<string, string>; label: string; exact?: boolean; count?: number }> }) {
  return (
    <nav className="mt-7 -mb-px flex gap-1 overflow-x-auto border-b border-rule [scrollbar-width:none]" aria-label="Sections">
      {items.map((t) => (
        <Link
          key={t.to}
          to={t.to as "/"}
          params={t.params as never}
          activeOptions={{ exact: !!t.exact, includeSearch: false }}
          className="relative flex h-10 shrink-0 items-center gap-2 px-3 text-[0.875rem] text-ink-3 transition-colors first:pl-0 first:after:left-0 hover:text-ink data-[status=active]:font-[550] data-[status=active]:text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-[2px] after:rounded-full after:bg-transparent data-[status=active]:after:bg-ink"
        >
          {t.label}
          {t.count !== undefined && <span className="text-xs text-ink-3 tnum">{t.count}</span>}
        </Link>
      ))}
    </nav>
  );
}

/** Nothing here yet: a dashed outline (like "room left" in the Box), a sentence, and what to do. */
export function Empty({ icon, title, children, className }: { icon?: ReactNode; title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn("rounded-[10px] border border-dashed border-rule-3 px-6 py-9 text-center", className)}>
      {icon && <div className="mx-auto mb-3 grid size-8 place-items-center text-ink-3 [&_svg]:size-5">{icon}</div>}
      <p className="text-md text-ink">{title}</p>
      {children && <div className="mx-auto mt-1.5 max-w-[30rem] text-base text-ink-3">{children}</div>}
    </div>
  );
}

/** Content written by apps, visitors or senders: shown as text, labelled as such. */
export function Untrusted({
  children,
  label = "Written by your apps and their visitors. Shown as plain text.",
  className,
}: {
  children: ReactNode;
  label?: string;
  className?: string;
}) {
  return (
    <div className={cn("overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk", className)}>
      <p className="flex items-center gap-2 border-b border-rule px-3 py-1.5 text-xs text-ink-3">
        <span aria-hidden className="shrink-0 font-mono whitespace-nowrap text-ink-3">
          {"{ }"}
        </span>
        {label}
      </p>
      {children}
    </div>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-[6px] bg-paper-sunk", className)} />;
}

/** For pages whose module only runs on a real box (tiffin serve --box). */
export function NotOnBox({ what }: { what: string }) {
  return (
    <Page>
      <h1 className="title text-ink">{what} live on a running box.</h1>
      <p className="mt-3 max-w-[34rem] text-md text-ink-2">
        This dashboard is talking to a local development server. Start the box with <code className="font-mono text-ink">tiffin serve --box</code> (or{" "}
        <code className="font-mono text-ink">tiffin up</code>) to use them.
      </p>
    </Page>
  );
}
