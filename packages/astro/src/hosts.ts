/**
 * The app's public hosts, from the box's environment.
 *
 * - TIFFIN_HOSTS: the app's hosts, separated by commas or spaces. An entry may
 *   be a bare host ("example.com"), a host with a port, a URL
 *   ("https://example.com"), or a wildcard ("*.example.com", "**.example.com").
 * - TIFFIN_URL: the app's own address. Used when TIFFIN_HOSTS is absent or
 *   empty, and its scheme is the protocol of bare hosts.
 *
 * Nothing else widens trust: no other variable is read.
 */

/** One entry of Astro's `security.allowedDomains`. */
export interface AllowedDomain {
  hostname?: string;
  protocol?: string;
  port?: string;
}

/** What the environment says about where the app is served. */
export interface AppHosts {
  /** The app's URL (TIFFIN_URL), normalised, or undefined. */
  url: string | undefined;
  /** Patterns for security.allowedDomains, in order, without duplicates. */
  domains: AllowedDomain[];
  /** Which variable the hosts came from. */
  source: "TIFFIN_HOSTS" | "TIFFIN_URL" | undefined;
}

type Env = Record<string, string | undefined>;

/** A URL's protocol without the colon ("https"), or undefined. */
function scheme(u: string): string | undefined {
  const m = /^([a-z][a-z0-9+.-]*):\/\//i.exec(u);
  return m?.[1]?.toLowerCase();
}

const HOST_RE = /^(\*\*?\.)?([a-z0-9-]+\.)*[a-z0-9-]+\.?$/i;

/** One TIFFIN_HOSTS entry as an allowedDomains pattern, or undefined when it isn't a host. */
export function parseHost(entry: string, defaultProtocol: string): AllowedDomain | undefined {
  let s = entry.trim();
  if (!s) return undefined;
  let protocol = defaultProtocol;
  const sch = scheme(s);
  if (sch) {
    if (sch !== "http" && sch !== "https") return undefined;
    protocol = sch;
    s = s.slice(sch.length + 3);
  }
  // Drop any path, query or user info: only the authority counts.
  s = s.split(/[/?#]/, 1)[0] ?? "";
  if (s.includes("@")) return undefined;
  let port: string | undefined;
  if (s.startsWith("[")) return undefined; // IPv6 literals: the box never serves an app on one
  const colon = s.lastIndexOf(":");
  if (colon >= 0) {
    port = s.slice(colon + 1);
    s = s.slice(0, colon);
    if (!/^\d{1,5}$/.test(port)) return undefined;
  }
  const hostname = s.toLowerCase().replace(/\.$/, "");
  if (!HOST_RE.test(hostname) || hostname === "*" || hostname === "**") return undefined;
  const d: AllowedDomain = { hostname, protocol };
  if (port && !((protocol === "https" && port === "443") || (protocol === "http" && port === "80"))) d.port = port;
  return d;
}

/** The app's URL and hosts from TIFFIN_HOSTS / TIFFIN_URL. */
export function appHosts(env: Env): AppHosts {
  const rawUrl = env.TIFFIN_URL?.trim();
  let url: string | undefined;
  let urlDomain: AllowedDomain | undefined;
  if (rawUrl) {
    try {
      const u = new URL(rawUrl);
      if (u.protocol === "http:" || u.protocol === "https:") {
        url = u.origin + (u.pathname === "/" ? "" : u.pathname.replace(/\/$/, ""));
        urlDomain = parseHost(u.host, u.protocol.slice(0, -1));
      }
    } catch {
      // not a URL: ignored
    }
  }
  const proto = url ? (scheme(url) ?? "https") : "https";
  const list = (env.TIFFIN_HOSTS ?? "").split(/[\s,]+/).filter(Boolean);
  const domains: AllowedDomain[] = [];
  for (const e of list) {
    const d = parseHost(e, proto);
    if (d) domains.push(d);
  }
  if (domains.length > 0) return { url, domains: dedupe(domains), source: "TIFFIN_HOSTS" };
  if (urlDomain) return { url, domains: [urlDomain], source: "TIFFIN_URL" };
  return { url, domains: [], source: undefined };
}

const keyOf = (d: AllowedDomain) => `${d.protocol ?? ""}://${d.hostname ?? ""}:${d.port ?? ""}`;

/** The list without repeats (same hostname, protocol and port). */
export function dedupe(list: AllowedDomain[]): AllowedDomain[] {
  const seen = new Set<string>();
  return list.filter((d) => {
    const k = keyOf(d);
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
}

/**
 * The entries of `ours` the user's list does not already have. Astro's
 * updateConfig appends arrays, so returning only these merges the two
 * without repeats; the user's entries are never removed or changed.
 */
export function missingDomains(user: readonly AllowedDomain[] | undefined, ours: AllowedDomain[]): AllowedDomain[] {
  const have = new Set((user ?? []).map(keyOf));
  return ours.filter((d) => !have.has(keyOf(d)));
}
