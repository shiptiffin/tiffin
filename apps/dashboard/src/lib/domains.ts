import { queryOptions } from "@tanstack/react-query";
import { ApiError, request, type Plan } from "@/api/client";
import type { components } from "@/api/schema";
import { sentence } from "@/components/problem";

type S = components["schemas"];
export type Domain = S["DomainsDomain"];
export type DomainChange = S["DomainsDomainChange"];
export type BoxDomain = S["DomainsBoxDomain"];
export type DomainCheck = S["DomainsDomainCheck"];
export type RecordCheck = S["DomainsRecordCheck"];
export type DnsRecord = S["Record"];
export type Providers = S["DomainsProviders"];
export type ConnectedProvider = S["DomainsConnectedProvider"];
/** What removing a domain or deleting a project does to DNS records. */
export type DnsCleanup = S["DNSCleanup"];

const enc = encodeURIComponent;
const projectPath = (p: string) => `/v1/projects/${enc(p)}/domains`;

// ───────────────────────── queries ─────────────────────────

/** A project's own domains. Refreshes quickly while one is on its way to live. */
export const projectDomainsQuery = (project: string) =>
  queryOptions({
    queryKey: ["domains", project],
    queryFn: async () => (await request<Domain[] | null>("GET", projectPath(project))) ?? [],
    retry: false,
    refetchInterval: (qq) => (qq.state.error ? false : (qq.state.data ?? []).some((d) => d.state !== "live") ? 5_000 : 60_000),
  });

/** The box's domain: the automatic address, or your own. */
export const boxDomainQuery = queryOptions({
  queryKey: ["box-domain"],
  queryFn: () => request<BoxDomain>("GET", "/v1/domain"),
  retry: false,
  staleTime: 15_000,
});

/** Connected DNS providers (box admins only; a 403 means "not yours to see"). */
export const dnsProvidersQuery = queryOptions({
  queryKey: ["dns-providers"],
  queryFn: () => request<Providers>("GET", "/v1/dns/providers"),
  retry: false,
  staleTime: 30_000,
});

export const checkBoxDomain = (domain: string, appsDomain = "") =>
  request<DomainCheck>("GET", `/v1/domain/check?domain=${enc(domain)}${appsDomain ? `&appsDomain=${enc(appsDomain)}` : ""}`);

/** Switches the box to `domain` (apps to `appsDomain` when given). 412 (thrown) when DNS isn't ready yet. */
export const setBoxDomain = (domain: string, appsDomain = "") => request<BoxDomain>("POST", "/v1/domain", { domain, ...(appsDomain ? { appsDomain } : {}) });
export const unsetBoxDomain = () => request<BoxDomain>("DELETE", "/v1/domain");

export const recheckDomain = (project: string, domain: string) => request<Domain>("POST", `${projectPath(project)}/${enc(domain)}/check`);

/** Sets records through the connected DNS provider that holds their zone. */
export const setRecords = (records: DnsRecord[]) =>
  request<{ set: DnsRecord[] | null; summary: string }>("PUT", "/v1/dns/records", {
    records: records.map((r) => ({ name: r.name, type: r.type, value: r.value, ...(r.ttl ? { ttl: r.ttl } : {}) })),
  });

export const connectDns = (provider: string, token: string) => request<Providers>("PUT", `/v1/dns/providers/${enc(provider)}`, { token });
export const disconnectDns = (provider: string) => request<Providers>("DELETE", `/v1/dns/providers/${enc(provider)}`);

/**
 * Domain add and remove go through plan and apply like any change: the first
 * call answers 428 with the plan, the second applies that plan's hash. The
 * person already said what they want by clicking, so this does both.
 */
async function planned<T>(call: (confirm?: string) => Promise<T>, reviewed?: string): Promise<T> {
  try {
    return await call(reviewed);
  } catch (e) {
    const plan = e instanceof ApiError && e.status === 428 ? (e.problem.plan as Plan | undefined) : undefined;
    if (!plan?.hash) throw e;
    return call(plan.hash);
  }
}

type AddBody = { domain: string; app: string; www?: boolean; createRecords?: boolean };

/**
 * Adds a domain. With `reviewed` (the hash of a plan the person saw, from
 * planAddDomain), that plan is applied; if it changed meanwhile, the new one is.
 */
export const addDomain = (project: string, body: AddBody, reviewed?: string) =>
  planned(
    (confirm) => request<DomainChange>("POST", projectPath(project), { ...body, ...(confirm ? { confirm, intent: `Serve ${body.domain} from ${body.app}` } : {}) }),
    reviewed,
  );

/** What adding a domain would do, without doing it: the plan's hash and the DNS records it needs. */
export async function planAddDomain(project: string, body: AddBody): Promise<{ hash?: string; records: DnsRecord[] }> {
  try {
    await request<DomainChange>("POST", projectPath(project), body);
    return { records: [] }; // nothing to change: it is there already
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 428) throw e;
    const plan = e.problem.plan as Plan | undefined;
    return { hash: plan?.hash, records: (e.problem as { records?: DnsRecord[] | null }).records ?? [] };
  }
}

/**
 * Removes a domain. With `reviewed` (the hash from planRemoveDomain), that
 * plan is applied. The answer's dns says which records the box deleted.
 */
export const removeDomain = (project: string, domain: string, reviewed?: string) =>
  planned(
    (confirm) => request<DomainChange>("POST", `${projectPath(project)}/${enc(domain)}/remove`, confirm ? { confirm, intent: `Stop serving ${domain}` } : {}),
    reviewed,
  );

/** What removing a domain would do, without doing it: the plan's hash and what happens to its DNS records. */
export async function planRemoveDomain(project: string, domain: string): Promise<{ hash?: string; dns?: DnsCleanup }> {
  try {
    await request<DomainChange>("POST", `${projectPath(project)}/${enc(domain)}/remove`, {});
    return {};
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 428) throw e;
    return { hash: (e.problem.plan as Plan | undefined)?.hash, dns: e.problem.dns ?? undefined };
  }
}

// ───────────────────────── words ─────────────────────────

export type Tone = "ok" | "busy" | "wait" | "bad";

/** A domain's state as a person says it: "Live with HTTPS", "Waiting for DNS", "Getting a certificate", "Problem: …". */
export function stateWords(d: Pick<Domain, "state" | "reason">): { word: string; tone: Tone; detail?: string } {
  switch (d.state) {
    case "live":
      return { word: "Live with HTTPS", tone: "ok", detail: d.reason ? reasonWords(d.reason) : undefined };
    case "issuing":
      return { word: "Getting a certificate", tone: "busy", detail: "It points at your box. This usually takes under a minute." };
    case "error": {
      // The line says what's wrong; the fix (the next sentence) goes in the panel below it.
      const all = reasonWords(d.reason ?? "something went wrong");
      return { word: `Problem: ${all.split(/(?<=[.!?])\s/)[0].replace(/\.$/, "")}`, tone: "bad", detail: all };
    }
    default:
      return { word: "Waiting for DNS", tone: "wait", detail: d.reason ? reasonWords(d.reason) : undefined };
  }
}

/** The box's reasons, in plain words with the fix. Unknown ones pass through as a sentence. */
export function reasonWords(reason: string): string {
  const r = reason.trim();
  let m: RegExpMatchArray | null;
  if (/^not checked yet/i.test(r)) return "Not checked yet.";
  if (/no public ip/i.test(r)) return "This box runs on your computer, so no domain can point at it. Your own domains need a box on a server.";
  if (/no A or AAAA record/i.test(r)) return "No records for it yet.";
  if (/cloudflare's proxy/i.test(r)) return "Cloudflare is proxying it. Set its records to “DNS only” (the grey cloud) in Cloudflare.";
  if ((m = r.match(/has an AAAA record \(([^)]+)\) but this box has no IPv6/i))) return `It has an IPv6 address (${m[1]}), but your box has none. Delete its AAAA record at your DNS host.`;
  if ((m = r.match(/points to (.+?), not this box(?: \((.+)\))?$/i))) return `It points to ${m[1]}, not to your box${m[2] ? ` (${m[2]})` : ""}.`;
  if ((m = r.match(/alias of (\S+), which has no address/i))) return `It’s an alias of ${m[1]}, which doesn’t lead anywhere yet.`;
  if (/could not ask DNS/i.test(r)) return "DNS didn’t answer just now. The box asks again on its own.";
  if (/rate limit|ratelimited|too many/i.test(r)) return "Let’s Encrypt asked the box to slow down. It tries again on its own, so there’s nothing to do.";
  if (/caa/i.test(r)) return "A CAA record at your DNS host doesn’t allow Let’s Encrypt. Allow letsencrypt.org there.";
  if (/ports 80 and 443|connection refused|timeout/i.test(r)) return "Let’s Encrypt couldn’t reach your box. Open ports 80 and 443 in the server’s firewall.";
  if (/renewal is failing/i.test(r)) return "Renewing its certificate keeps failing. The current one still works for now.";
  if (/dns check failed|dns problem|nxdomain/i.test(r)) return "Let’s Encrypt couldn’t see the records yet. They may still be spreading.";
  return sentence(r);
}

/** "example.com" is a whole domain; "shop.example.com" is a name under one. Close enough for the www offer. */
export function isApex(domain: string): boolean {
  const labels = domain.split(".").filter(Boolean);
  if (labels.length === 2) return true;
  return labels.length === 3 && /^(co|com|org|net|ac|gov|edu)\.[a-z]{2}$/.test(labels.slice(1).join("."));
}

/** What a person typed, as the box will want it: "https://Example.com/" → "example.com". */
export function cleanDomain(s: string): string {
  return s
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//, "")
    .replace(/[/:].*$/, "")
    .replace(/\.$/, "");
}

export const looksLikeDomain = (d: string) => /^(?!-)[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(d) && !d.includes("..") && d.length <= 253;

/** The project's domains with each www.<domain> folded under its domain. */
export function withWww(list: Domain[]): Array<{ d: Domain; www?: Domain }> {
  const redirects = new Map(list.filter((d) => d.redirectTo).map((d) => [d.redirectTo!, d]));
  const rows: Array<{ d: Domain; www?: Domain }> = list.filter((d) => !d.redirectTo).map((d) => ({ d, www: redirects.get(d.domain) }));
  // A www redirect whose domain isn't served here (it shouldn't happen): show it on its own.
  for (const [to, w] of redirects) if (!rows.some((r) => r.d.domain === to)) rows.push({ d: w });
  return rows;
}

/** Where to add records at the usual DNS hosts, one line each. */
export const HOSTS: Array<{ name: string; how: string }> = [
  { name: "Cloudflare", how: "Your domain › DNS › Records › Add record. Set Proxy status to “DNS only” (grey cloud)." },
  { name: "Namecheap", how: "Domain List › Manage › Advanced DNS › Add New Record." },
  { name: "GoDaddy", how: "My Products › your domain › DNS › Add New Record." },
  { name: "Squarespace", how: "Domains › your domain › DNS › Custom records › Add record." },
  { name: "Porkbun", how: "Domain Management › your domain › DNS › add each record." },
  { name: "Route 53", how: "Hosted zones › your domain › Create record." },
  { name: "Other", how: "Look for “DNS”, “DNS records” or “Zone editor” where you bought the domain." },
];

/** A DNS provider by its name in the interface. */
export const providerName = (p?: string) => (p === "cloudflare" ? "Cloudflare" : (p ?? "Your DNS provider"));

export const CLOUDFLARE_TOKENS = "https://dash.cloudflare.com/profile/api-tokens";

// ───────────────────────── buying ─────────────────────────

export type Offer = S["DomainsOffer"];
export type SearchResult = S["DomainsSearchResult"];
export type Owned = S["DomainsOwned"];
export type OwnedDomain = S["DomainsOwnedDomain"];
export type Purchase = S["DomainsPurchase"];
export type BuyReadiness = S["DomainsBuyReadiness"];
export type Quote = S["Quote"];

/**
 * Names like q (Cloudflare Registrar, in the box's Cloudflare account), as soon
 * as Cloudflare's search answers: the name itself is checked live, the rest
 * come from Cloudflare's cache until domainCheckQuery confirms them.
 */
export const domainSearchQuery = (q: string) =>
  queryOptions({
    queryKey: ["domain-search", q],
    queryFn: ({ signal }) => request<SearchResult>("GET", `/v1/domains/search?q=${enc(q)}&quick=true`, undefined, signal),
    enabled: q.length > 0,
    retry: false,
    staleTime: 60_000,
  });

/** Live availability and prices for names a quick search showed from cache. */
export const domainCheckQuery = (names: string[]) =>
  queryOptions({
    queryKey: ["domain-check", names],
    queryFn: ({ signal }) => request<{ offers: Offer[] | null }>("POST", "/v1/domains/check", { domains: names }, signal),
    enabled: names.length > 0,
    retry: false,
    staleTime: 60_000,
  });

/** Every domain the box can put on projects, with where each is used. */
export const ownedDomainsQuery = queryOptions({
  queryKey: ["domains-owned"],
  queryFn: () => request<Owned>("GET", "/v1/domains/owned"),
  retry: false,
  staleTime: 15_000,
  refetchInterval: (qq) => (qq.state.data?.domains ?? []).some((d) => d.purchase && !["ready", "failed"].includes(d.purchase.state)) ? 4_000 : false,
});

/** A purchase's progress; polls until it settles. */
export const purchaseQuery = (domain: string) =>
  queryOptions({
    queryKey: ["domain-purchase", domain],
    queryFn: () => request<Purchase>("GET", `/v1/domains/buy/${enc(domain)}`),
    retry: false,
    refetchInterval: (qq) => (["ready", "failed"].includes(qq.state.data?.state ?? "") ? false : qq.state.data?.state === "blocked" || qq.state.data?.state === "action_required" ? 10_000 : 2_000),
  });

export type BuyBody = { domain: string; years?: number; autoRenew: boolean; price?: string; project?: string; app?: string };

/** Buys a domain at `price`. A 428 (thrown, problem.quote) means the price to confirm differs. */
export const buyDomain = (body: BuyBody) => request<Purchase>("POST", "/v1/domains/buy", body);

/** The quote in a 428 from buyDomain, if that's what it was. */
export function quoteOf(e: unknown): Quote | undefined {
  return e instanceof ApiError && e.status === 428 ? ((e.problem as { quote?: Quote }).quote ?? undefined) : undefined;
}

/** "$10.44", or "10.44 EUR" when the currency has no symbol here. */
export function price(amount?: string, currency = "USD"): string {
  if (!amount) return "";
  const n = Number(amount);
  if (!Number.isFinite(n)) return `${amount} ${currency}`;
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, minimumFractionDigits: 2 }).format(n);
  } catch {
    return `${amount} ${currency}`;
  }
}

/** "acme" and ".app": a name's label and its ending, for typesetting. */
export function splitName(d: string): [string, string] {
  const i = d.indexOf(".");
  return i < 0 ? [d, ""] : [d.slice(0, i), d.slice(i)];
}
