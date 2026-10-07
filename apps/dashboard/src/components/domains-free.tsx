import { useQueries } from "@tanstack/react-query";
import { ArrowUpRight } from "lucide-react";
import type { Manifest } from "@/api/client";
import { mod3 } from "@/api/modules";
import { CopyButton } from "@/components/copy";
import { DOMAIN_COLS } from "@/components/domains-row";
import { shortDate, untilWords } from "@/components/domains-parts";
import { StatusDot } from "@/components/project-domains";
import { cn } from "@/lib/cn";
import type { BoxDomain } from "@/lib/domains";

type Apps = Record<string, { routes?: string[] | null; role?: string }>;

/** Each web app's names under the box's apps domain ("hello", "hello/api"), as full addresses. */
export function boxAddresses(manifest: Manifest | undefined, appsDomain: string): Array<{ app: string; host: string; path: string }> {
  const apps = ((manifest as unknown as { apps?: Apps } | undefined)?.apps ?? {}) as Apps;
  const out: Array<{ app: string; host: string; path: string }> = [];
  for (const [app, a] of Object.entries(apps)) {
    if (a.role === "worker") continue;
    for (const r of a.routes ?? []) {
      const [name, ...rest] = r.split("/");
      if (name.includes(".")) continue; // a domain of your own: listed above
      const path = rest.join("/").replace(/\/+$/, "");
      out.push({ app, host: `${name}.${appsDomain}`, path: path ? `/${path}` : "" });
    }
  }
  return out.sort((x, y) => x.host.localeCompare(y.host) || x.path.localeCompare(y.path));
}

/**
 * The addresses every project gets on the box, free and always on: the
 * project's name under the box's apps domain. Their certificate is the
 * box's (one wildcard, or one each on first visit), so nothing to set up.
 */
export function FreeAddresses({ project, manifest, box }: { project: string; manifest?: Manifest; box?: BoxDomain }) {
  const list = box ? boxAddresses(manifest, box.appsDomain) : [];
  const apps = [...new Set(list.map((a) => a.app))];
  const rts = useQueries({ queries: apps.map((a) => ({ queryKey: ["runtime", project, a], queryFn: () => mod3.runtime(project, a), retry: false, staleTime: 60_000 })) });
  const urlOf = new Map(apps.map((a, i) => [a, rts[i]?.data?.production?.url]));
  const deployed = new Map(apps.map((a, i) => [a, rts[i]?.data ? !!rts[i].data.production : undefined]));
  if (!box || list.length === 0) return null;

  const https = box.certificates === "internal" ? "local" : box.wildcard?.certificate.state === "live" ? "wildcard" : "visit";
  const wild = box.wildcard?.certificate;

  return (
    <ul className="divide-y divide-rule border-y border-rule">
      {list.map((a) => {
        const base = urlOf.get(a.app);
        const href = base ? `${base.replace(/\/+$/, "")}${a.path}` : undefined;
        const live = deployed.get(a.app);
        const shown = `${a.host}${a.path}`;
        return (
          <li key={shown} className={cn("grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-0.5 py-3", DOMAIN_COLS)}>
            <div className="flex min-w-0 items-center gap-2.5">
              <StatusDot tone={live ? "ok" : "wait"} className="mx-[3px]" />
              <span className="min-w-0">
                <span className="flex min-w-0 items-center gap-0.5">
                  {href ? (
                    <a href={href} target="_blank" rel="noopener noreferrer" className="group ident inline-flex min-w-0 items-center gap-1 text-[0.875rem] text-ink hover:text-brass-ink">
                      <span className="truncate">{shown}</span>
                      <ArrowUpRight className="size-3.5 shrink-0 text-ink-4 group-hover:text-brass-ink" />
                    </a>
                  ) : (
                    <span className="ident truncate text-[0.875rem] text-ink">{shown}</span>
                  )}
                  <CopyButton value={href ?? `https://${shown}`} label={`Copy ${shown}`} className="size-6" />
                </span>
                <span className="block truncate text-xs text-ink-3 sm:hidden">
                  {live === false ? "Not deployed yet" : "Live"} · shows {a.app}
                </span>
              </span>
            </div>
            <span className="ident text-[0.75rem] text-ink max-sm:hidden">{a.app}</span>
            <span className="text-[0.8125rem] max-sm:hidden">
              {live === false ? <span className="text-ink-3">Not deployed yet</span> : <span className="text-ink">Live</span>}
              <span className="block text-xs text-ink-3">Free, always on</span>
            </span>
            <span className="text-[0.8125rem] max-sm:hidden">
              {https === "local" ? (
                <>
                  <span className="text-ink-2">Box’s own</span>
                  <span className="block text-xs text-ink-3">Trusted on this computer</span>
                </>
              ) : https === "wildcard" && wild?.notAfter ? (
                <span title={`*.${box.appsDomain} · ${wild.issuer ?? ""}`}>
                  <span className="text-ink-2">Until {shortDate(wild.notAfter)}</span>
                  <span className="block text-xs text-ink-3">Shared · {untilWords(wild.notAfter)}</span>
                </span>
              ) : (
                <>
                  <span className="text-ink-2">Automatic</span>
                  <span className="block text-xs text-ink-3">Issued on first visit</span>
                </>
              )}
            </span>
            <span aria-hidden className="max-sm:hidden" />
          </li>
        );
      })}
    </ul>
  );
}
