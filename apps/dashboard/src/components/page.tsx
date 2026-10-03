import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

/**
 * One frame for every page, so the title never jumps sideways between pages:
 * the frame is centred, and narrower pages stay left-aligned inside it.
 */
export function Page({ children, className, wide, full }: { children: ReactNode; className?: string; wide?: boolean; full?: boolean }) {
  return (
    <div className="mx-auto w-full max-w-[80rem] px-4 pt-10 pb-16 sm:px-8 sm:pt-14 lg:px-10">
      <div className={cn("w-full", full ? "" : wide ? "max-w-5xl" : "max-w-[52rem]", className)}>{children}</div>
    </div>
  );
}

/** "shop › Storage": where a page sits. The last part is the page itself, so it isn't repeated. */
export function Crumbs({ items }: { items: Array<{ label: ReactNode; to?: string; params?: Record<string, string>; mono?: boolean }> }) {
  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5">
      {items.map((c, i) => (
        <span key={i} className="flex min-w-0 items-center gap-1.5">
          {i > 0 && (
            <span aria-hidden className="text-ink-4">
              ›
            </span>
          )}
          {c.to ? (
            <Link to={c.to as "/"} params={c.params as never} className={cn("truncate hover:text-ink", c.mono && "font-mono")}>
              {c.label}
            </Link>
          ) : (
            <span className={cn("truncate", c.mono && "font-mono")}>{c.label}</span>
          )}
        </span>
      ))}
    </nav>
  );
}

/** Eyebrow, title, one line of why, actions on the right. */
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
    <header className="animate-rise">
      <div className="flex flex-col gap-5 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          {eyebrow && <p className="mb-1.5 text-sm text-ink-3">{eyebrow}</p>}
          <h1 className="display text-3xl text-ink">{title}</h1>
          {lede && <p className="mt-2 max-w-[38rem] text-md text-ink-2">{lede}</p>}
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
    <nav className="mt-8 -mb-px flex gap-1 overflow-x-auto border-b border-rule [scrollbar-width:none]" aria-label="Sections">
      {items.map((t) => (
        <Link
          key={t.to}
          to={t.to as "/"}
          params={t.params as never}
          activeOptions={{ exact: !!t.exact, includeSearch: false }}
          className="relative flex h-10 shrink-0 items-center gap-2 px-3 text-base text-ink-3 transition-colors hover:text-ink data-[status=active]:font-medium data-[status=active]:text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-transparent data-[status=active]:after:bg-ink"
        >
          {t.label}
          {t.count !== undefined && <span className="font-mono text-xs text-ink-4 tnum">{t.count}</span>}
        </Link>
      ))}
    </nav>
  );
}

export function Empty({ icon, title, children, className }: { icon?: ReactNode; title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn("rounded-xl border border-dashed border-rule-strong px-6 py-10 text-center", className)}>
      {icon && <div className="mx-auto mb-3 grid size-10 place-items-center rounded-xl bg-hover text-ink-3 [&_svg]:size-5">{icon}</div>}
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
    <div className={cn("overflow-hidden rounded-xl border border-rule bg-paper-sunk", className)}>
      <p className="flex items-center gap-2 border-b border-rule px-3 py-1.5 text-xs text-ink-3">
        <span aria-hidden className="font-mono text-ink-4">
          {"{ }"}
        </span>
        {label}
      </p>
      {children}
    </div>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-md bg-hover", className)} />;
}

/** For pages whose module only runs on a real box (tiffin serve --box). */
export function NotOnBox({ what }: { what: string }) {
  return (
    <Page>
      <h1 className="display text-3xl text-ink">{what} live on a running box.</h1>
      <p className="mt-3 max-w-[34rem] text-md text-ink-2">
        This dashboard is talking to a local development server. Start the box with <code className="font-mono text-ink">tiffin serve --box</code> (or{" "}
        <code className="font-mono text-ink">tiffin up</code>) to use them.
      </p>
    </Page>
  );
}
