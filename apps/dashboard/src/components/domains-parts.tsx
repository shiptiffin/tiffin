import { useNavigate, useSearch } from "@tanstack/react-router";
import { AlertTriangle, Check } from "lucide-react";
import { useEffect, useState } from "react";
import type { Row } from "@/components/dns-records";
import { cn } from "@/lib/cn";
import { isApex, type DnsRecord, type Domain } from "@/lib/domains";
import { relative } from "@/lib/time";

/**
 * Small pieces the Domains page shares between its list, the detail under a
 * row and the Add dialog: words for routes and certificates, the three steps
 * to live, a ticking "checked 12 seconds ago", and the ?domain= in the URL.
 */

// ───────────────────────── words ─────────────────────────

const dateFmt = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" });
export const shortDate = (iso: string) => dateFmt.format(new Date(iso));

/** "example.com" or "shop.example.com": which kind of name, for the records and the www offer. */
export const kindOf = (domain: string): "apex" | "sub" => (isApex(domain) && !domain.startsWith("www.") ? "apex" : "sub");

/** The app on "/" and the other paths, as the list shows them. */
export function routesOf(d: Domain): { root?: string; paths: Array<{ path: string; app: string }> } {
  const routes = d.routes ?? [];
  const root = routes.find((r) => r.path === "/" || r.path === "");
  return { root: root?.app, paths: routes.filter((r) => r !== root).map((r) => ({ path: r.path, app: r.app })) };
}

/** Where a step went wrong: DNS (it points elsewhere) or the certificate (CAA, rate limit, firewall). */
export function failedAt(d: Pick<Domain, "state" | "reason">): "dns" | "cert" | null {
  if (d.state !== "error") return null;
  return /points to|AAAA|proxy|alias|no A |could not ask DNS/i.test(d.reason ?? "") ? "dns" : "cert";
}

export type StepState = "done" | "now" | "todo" | "failed";

/** The three steps to live and where a domain is on them. */
export function stepsOf(d: Pick<Domain, "state" | "reason">): Array<{ key: string; label: string; short: string; state: StepState }> {
  const at = d.state === "live" ? 3 : d.state === "issuing" ? 1 : d.state === "error" ? (failedAt(d) === "dns" ? 0 : 1) : 0;
  const st = (i: number): StepState => (i < at ? "done" : i > at ? "todo" : d.state === "error" ? "failed" : d.state === "live" ? "done" : "now");
  return [
    { key: "dns", label: "DNS points to your box", short: "DNS", state: st(0) },
    { key: "cert", label: "HTTPS certificate", short: "Certificate", state: st(1) },
    { key: "live", label: "Live", short: "Live", state: d.state === "live" ? "done" : "todo" },
  ];
}

/** The records with a tick for each one DNS already answers (after the box has looked at least once). */
export function withTicks(d: Domain, recs: DnsRecord[]): Row[] {
  const norm = (s: string) => s.trim().toLowerCase().replace(/\.$/, "");
  const looked = !!d.checkedAt;
  const found = new Set((d.found ?? []).map(norm));
  const cname = norm(d.cname ?? "");
  return recs.map((r) => ({ ...r, ok: !looked ? undefined : r.type === "CNAME" ? cname === norm(r.value) : found.has(norm(r.value)) }));
}

/** "4 seconds ago", "a minute ago": finer than relative() for the first minute. */
export function agoWords(iso: string, now: number): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 3) return "just now";
  if (s < 60) return `${s} seconds ago`;
  return relative(iso, now);
}

/** "in 74 days", or the time it has been expired. */
export function untilWords(iso: string, now = Date.now()): string {
  const days = Math.round((new Date(iso).getTime() - now) / 86_400_000);
  if (days < 0) return `expired ${relative(iso, now)}`;
  if (days === 0) return "today";
  if (days === 1) return "tomorrow";
  return `in ${days} days`;
}

/** "@", "_dmarc" or "www" under the domain; the full name for anything else in the zone. */
export function relName(name: string, base: string) {
  if (name === base) return "@";
  if (name.endsWith(`.${base}`)) return name.slice(0, -base.length - 1);
  return name;
}

/** What a person typed for a name, as a full name: "@" is the domain, "_dmarc" is under it, a full name in the zone stays. */
export function toFullName(input: string, base: string, zone: string): string {
  const n = input.trim().toLowerCase().replace(/\.$/, "");
  if (n === "" || n === "@") return base;
  if (n === zone || n.endsWith(`.${zone}`)) return n;
  return `${n}.${base}`;
}

// ───────────────────────── hooks ─────────────────────────

/** The time now, ticking every `ms` while mounted. */
export function useNow(ms = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

/** The open domain in the URL (?domain=example.com), so a link or a reload lands on it. */
export function useOpenDomain(project: string): [string | undefined, (d: string | undefined) => void] {
  const search = useSearch({ strict: false }) as { domain?: unknown };
  const navigate = useNavigate();
  const open = typeof search.domain === "string" ? search.domain : undefined;
  const set = (d: string | undefined) =>
    void navigate({
      to: "/projects/$project/domains",
      params: { project },
      search: (d ? { domain: d } : {}) as never,
      replace: true,
      resetScroll: false,
    });
  return [open, set];
}

// ───────────────────────── marks ─────────────────────────

/** The three steps to live in one line: a tick for each done, a spinner on the current one, a warning where it stopped. */
export function Steps({ d, className }: { d: Pick<Domain, "state" | "reason">; className?: string }) {
  const steps = stepsOf(d);
  return (
    <ol className={cn("flex max-w-[44rem] items-center gap-2 text-[0.8125rem]", className)} aria-label="Steps to live">
      {steps.map((s, i) => (
        <li key={s.key} className={cn("flex min-w-0 items-center gap-2", i < steps.length - 1 ? "flex-1" : "")}>
          <StepMark state={s.state} />
          <span
            className={cn(
              "truncate",
              s.state === "failed" ? "font-[550] text-danger" : s.state === "now" ? "font-[550] text-ink" : s.state === "done" ? "text-ink-2" : "text-ink-3",
            )}
          >
            <span className="sm:hidden">{s.short}</span>
            <span className="max-sm:hidden">{s.label}</span>
            <span className="sr-only">
              {s.state === "done" ? " (done)" : s.state === "now" ? " (in progress)" : s.state === "failed" ? " (stopped here)" : " (next)"}
            </span>
          </span>
          {i < steps.length - 1 && <span aria-hidden className={cn("h-px min-w-3 flex-1", s.state === "done" ? "bg-ok/50" : "bg-rule-2")} />}
        </li>
      ))}
    </ol>
  );
}

function StepMark({ state }: { state: StepState }) {
  if (state === "done")
    return (
      <span aria-hidden className="grid size-4 shrink-0 place-items-center rounded-full bg-ok-wash text-ok">
        <Check className="size-3" strokeWidth={3} />
      </span>
    );
  if (state === "now")
    return (
      <span aria-hidden className="grid size-4 shrink-0 place-items-center">
        <span className="spinner size-3.5! text-brass" />
      </span>
    );
  if (state === "failed")
    return (
      <span aria-hidden className="grid size-4 shrink-0 place-items-center text-danger">
        <AlertTriangle className="size-3.5" strokeWidth={2.25} />
      </span>
    );
  return <span aria-hidden className="size-4 shrink-0 rounded-full shadow-[inset_0_0_0_1.5px_var(--rule-3)]" />;
}
