import type { Manifest } from "@/api/client";
import type { Domain } from "@/lib/domains";
import { changeMany, type StagedEdit } from "@/lib/staged";

/**
 * The primary domain: domains: { "example.com": { primary: true } }. Once it
 * is live, the app's other addresses redirect to it (308). Setting it is a
 * manifest edit like any other, with plan, apply and Undo.
 */

type Entry = { www?: string; primary?: boolean };
type Apps = Record<string, { routes?: string[] | null }>;

const domainsOf = (m?: Manifest) => ((m as unknown as { domains?: Record<string, Entry> } | undefined)?.domains ?? {}) as Record<string, Entry>;
const appsOf = (m?: Manifest) => ((m as unknown as { apps?: Apps } | undefined)?.apps ?? {}) as Apps;

/** The app that serves all of host (a route without a path). */
function wholeApp(m: Manifest | undefined, host: string): string | undefined {
  for (const [name, a] of Object.entries(appsOf(m)).sort(([x], [y]) => x.localeCompare(y))) {
    if ((a.routes ?? []).some((r) => r.replace(/\/+$/, "").toLowerCase() === host)) return name;
  }
  return undefined;
}

/** An entry with one option changed; undefined when none is left. */
function withOption(e: Entry | undefined, key: keyof Entry, value: string | boolean | undefined): Entry | undefined {
  const next: Entry = { ...e };
  if (value === undefined || value === false) delete next[key];
  else (next as Record<string, unknown>)[key] = value;
  return Object.keys(next).length ? next : undefined;
}

/** The edit that sets one option of a domain's entry and keeps the others (www and primary share it). */
export function domainOptionEdit(m: Manifest | undefined, domain: string, key: keyof Entry, value: string | boolean | undefined, what: string, undo: string): StagedEdit {
  const from = domainsOf(m)[domain];
  return { kind: "set", path: ["domains", domain], from, to: withOption(from, key, value), what, undo };
}

/** The edits that make a domain its app's primary (and unset the app's previous one), or stop it. */
export function primaryEdits(m: Manifest | undefined, domain: string, on: boolean): StagedEdit[] {
  const app = wholeApp(m, domain);
  const edits: StagedEdit[] = [];
  if (on) {
    for (const [k, e] of Object.entries(domainsOf(m))) {
      if (k !== domain && e.primary && wholeApp(m, k) === app) {
        edits.push(domainOptionEdit(m, k, "primary", undefined, `${k} is no longer the primary domain`, `${k} is the primary domain again`));
      }
    }
  }
  edits.push(
    on
      ? domainOptionEdit(m, domain, "primary", true, `Make ${domain} the primary domain of ${app ?? "its app"}`, `${domain} is no longer the primary domain`)
      : domainOptionEdit(m, domain, "primary", undefined, `${domain} is no longer the primary domain`, `${domain} is the primary domain again`),
  );
  return edits;
}

export const setPrimary = (project: string, m: Manifest | undefined, d: Domain, on: boolean) => changeMany(project, primaryEdits(m, d.domain, on));

/** Whether a domain can be primary: its app serves all of it. */
export const canBePrimary = (m: Manifest | undefined, d: Domain) => !d.redirectTo && !!wholeApp(m, d.domain);

/** The primary redirect in words, for the details of a domain. */
export function PrimaryFact({ d }: { d: Domain }) {
  if (d.primary) {
    const from = d.redirectFrom ?? [];
    if (!from.length) return <span className="text-ink-2">Yes. The app has no other address to redirect.</span>;
    return (
      <span className="text-ink-2">
        Yes. <span className="ident text-[0.8125rem]">{from.join(", ")}</span> {d.redirecting ? "redirect here (308)." : "will redirect here once it is live. Until then they keep serving."}
      </span>
    );
  }
  if (d.primaryDomain) {
    return (
      <span className="text-ink-2">
        No. {d.redirecting ? "It redirects to" : "Once it is live, it will redirect to"} <span className="ident text-[0.8125rem]">{d.primaryDomain}</span>
        {d.redirecting ? " (308)." : "."}
      </span>
    );
  }
  return <span className="text-ink-3">No. Make it primary and the app’s other addresses redirect here.</span>;
}
