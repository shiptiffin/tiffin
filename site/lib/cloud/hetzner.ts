// The Hetzner key check on /start: is the key real and read & write, what
// does the project already hold, and what can it order where, at the
// customer's own prices and stock. Read-only calls, plus one write that
// creates nothing (an SSH key with no key in it, which Hetzner refuses with
// 400 for a read & write key and 403 for a read-only one). Every call is
// recorded for the customer. Creating the server is the provisioner's job
// (Go, internal/provider/hetzner), never this file's.

export const HETZNER_API = "https://api.hetzner.cloud/v1";

export type Call = { at: Date; method: string; path: string; status: number | null; ms: number; error: string | null };

/** The sizes we offer, in the order we suggest them. */
export const EU_TYPES = ["cx23", "cax11", "cx33", "cax21"] as const;
export const EU_LOCATIONS = ["fsn1", "nbg1", "hel1"] as const;
export const US_TYPES = ["cpx22", "cpx32", "cpx21", "cpx31"] as const;
export const US_LOCATIONS = ["ash", "hil"] as const;
export const VOLUME_GB = 40;
/** Sizes a box can be resized to (Hetzner keeps the architecture: cx↔cx, cax↔cax, cpx↔cpx). */
export const RESIZE_TYPES = ["cx23", "cx33", "cx43", "cx53", "cax11", "cax21", "cax31", "cax41", "cpx22", "cpx32", "cpx42", "cpx21", "cpx31", "cpx41"] as const;
export const family = (t: string) => t.replace(/\d+$/, "");

export type Option = {
  serverType: string;
  arch: "arm64" | "amd64";
  cores: number;
  memoryGB: number;
  diskGB: number;
  location: string;
  city: string;
  country: string;
  region: "eu" | "us";
  available: boolean;
  /** Server + IPv4 + the 40 GB data volume, a month, before VAT. */
  monthlyNet: number;
  monthlyGross: number;
};

export type CheckResult =
  | { ok: true; readWrite: true; currency: string; vatRate: string; servers: number; options: Option[]; suggested: Option | null }
  | { ok: false; message: string };

type Price = { net: string; gross: string };
type ServerTypeJSON = {
  name: string;
  cores: number;
  memory: number;
  disk: number;
  architecture: string;
  prices?: { location: string; price_monthly: Price }[];
  locations?: { name: string; available?: boolean; deprecation?: unknown }[];
};
type LocationJSON = { name: string; city: string; country: string; network_zone?: string };
type PricingJSON = {
  currency: string;
  vat_rate: string;
  volume?: { price_per_gb_month: Price };
  primary_ips?: { type: string; prices: { location: string; price_monthly: Price }[] }[];
};

const num = (s: string | undefined) => (s ? Number(s) : 0);
const round2 = (n: number) => Math.round(n * 100) / 100;

/** The options we offer from what the API said, cheapest-first within our suggested order. */
export function buildOptions(types: ServerTypeJSON[], locations: LocationJSON[], pricing: PricingJSON): Option[] {
  const out: Option[] = [];
  const ip = (loc: string) => {
    const p = pricing.primary_ips?.find((x) => x.type === "ipv4")?.prices.find((x) => x.location === loc)?.price_monthly;
    return { net: num(p?.net), gross: num(p?.gross) };
  };
  const vol = { net: num(pricing.volume?.price_per_gb_month.net) * VOLUME_GB, gross: num(pricing.volume?.price_per_gb_month.gross) * VOLUME_GB };
  const add = (names: readonly string[], locs: readonly string[], region: "eu" | "us") => {
    for (const name of names) {
      const t = types.find((x) => x.name === name);
      if (!t) continue;
      for (const loc of locs) {
        const l = locations.find((x) => x.name === loc);
        const where = t.locations?.find((x) => x.name === loc);
        const price = t.prices?.find((x) => x.location === loc)?.price_monthly;
        if (!l || !where || !price || where.deprecation) continue; // not sold there at all
        const i = ip(loc);
        out.push({
          serverType: t.name,
          arch: t.architecture === "arm" ? "arm64" : "amd64",
          cores: t.cores,
          memoryGB: t.memory,
          diskGB: t.disk,
          location: loc,
          city: l.city,
          country: l.country,
          region,
          available: where.available !== false,
          monthlyNet: round2(num(price.net) + i.net + vol.net),
          monthlyGross: round2(num(price.gross) + i.gross + vol.gross),
        });
      }
    }
  };
  add(EU_TYPES, EU_LOCATIONS, "eu");
  add(US_TYPES, US_LOCATIONS, "us");
  return out;
}

/** Our default: cx23 where it is in stock, then cax11, cx33, cax21 (EU). */
export function suggest(options: Option[]): Option | null {
  for (const t of EU_TYPES) {
    const o = options.find((x) => x.serverType === t && x.region === "eu" && x.available);
    if (o) return o;
  }
  return options.find((x) => x.available) ?? null;
}

/** Calls the Hetzner API with the key, recording every request. */
export function hetznerClient(token: string, record: (c: Call) => Promise<void> | void, base = process.env.HCLOUD_ENDPOINT || HETZNER_API) {
  return async function call(method: string, path: string, body?: unknown): Promise<{ status: number; json: any }> {
    const started = Date.now();
    const at = new Date();
    try {
      const res = await fetch(base.replace(/\/+$/, "") + path, {
        method,
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", "User-Agent": "shiptiffin-control-plane" },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(15_000),
      });
      const json = await res.json().catch(() => null);
      await record({ at, method, path: "/v1" + path, status: res.status, ms: Date.now() - started, error: null });
      return { status: res.status, json };
    } catch (e) {
      await record({ at, method, path: "/v1" + path, status: null, ms: Date.now() - started, error: e instanceof Error ? e.message.slice(0, 300) : "failed" });
      throw e;
    }
  };
}

/** Checks a pasted key and lists what it can order. */
export async function checkToken(token: string, record: (c: Call) => Promise<void> | void, base?: string): Promise<CheckResult> {
  token = token.trim();
  if (!/^[A-Za-z0-9]{20,128}$/.test(token)) {
    return { ok: false, message: "That doesn't look like a Hetzner API token. Copy the whole token Hetzner showed you (letters and digits only)." };
  }
  const call = hetznerClient(token, record, base);
  let st;
  try {
    st = await call("GET", "/server_types?per_page=50");
  } catch {
    return { ok: false, message: "We couldn't reach Hetzner just now. Try again in a minute." };
  }
  if (st.status === 401) return { ok: false, message: "Hetzner didn't accept this token. Check you copied all of it, and that it hasn't been deleted." };
  if (st.status !== 200) return { ok: false, message: `Hetzner answered ${st.status}. Try again in a minute.` };
  const [locs, pricing, servers, probe] = await Promise.all([
    call("GET", "/locations"),
    call("GET", "/pricing"),
    call("GET", "/servers?per_page=1"),
    // Creates nothing: an SSH key needs a name and a key. Read & write → 400/422, read-only → 403.
    call("POST", "/ssh_keys", {}),
  ]);
  if (probe.status === 403) {
    return { ok: false, message: "This token is read-only. Make a new one with Read & Write permission (Security → API tokens → Generate API token)." };
  }
  if (locs.status !== 200 || pricing.status !== 200) return { ok: false, message: "Hetzner didn't send prices just now. Try again in a minute." };
  const options = buildOptions(st.json?.server_types ?? [], locs.json?.locations ?? [], pricing.json?.pricing ?? {});
  return {
    ok: true,
    readWrite: true,
    currency: pricing.json?.pricing?.currency ?? "EUR",
    vatRate: pricing.json?.pricing?.vat_rate ?? "",
    servers: Number(servers.json?.meta?.pagination?.total_entries ?? servers.json?.servers?.length ?? 0),
    options,
    suggested: suggest(options),
  };
}
