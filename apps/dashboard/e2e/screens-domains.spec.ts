import { mkdirSync } from "node:fs";
import { test, type Page, type Route } from "@playwright/test";
import { needsServices, signIn } from "./helpers";

// Visual review of domains: Project › Settings › Domains (every state), the
// overview's Domains, Settings › Your box › Domain (automatic, checking,
// ready to switch, your own) and Settings › DNS (Cloudflare), against a
// seeded dev box. A box in a local VM has no public address, so the domain
// and DNS answers are stubbed as a server's would be:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5391 E2E_OWNER_TOKEN=... bunx playwright test screens-domains
// It changes nothing on the box.
test.skip(!process.env.SCREENS, "set SCREENS=1 for screenshots");
needsServices("shop", []);

const out = process.env.SHOTS_DIR ?? "screenshots/domains";
const only = process.env.SHOTS?.split(",");

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

const IP4 = "203.0.113.7";
const IP6 = "2a01:4f8:c012:7d1b::1";
const BOX = "46-224-210-97.sslip.io";
const ago = (s: number) => new Date(Date.now() - s * 1000).toISOString();
const soon = (s: number) => new Date(Date.now() + s * 1000).toISOString();
const cert = (host: string, state = "live") => ({ host, state, checkedAt: ago(30), issuer: "Let's Encrypt", notAfter: soon(80 * 86400) });

const addr = (name: string, host: string) => [
  { name, host, type: "A", value: IP4 },
  { name, host, type: "AAAA", value: IP6 },
];

function dom(domain: string, state: string, extra: Record<string, unknown> = {}) {
  const sub = domain.split(".").length > 2 && !domain.startsWith("www.");
  return {
    domain,
    project: "shop",
    routes: [{ app: "web", path: "/" }],
    wwwRedirect: false,
    state,
    since: ago(600),
    records: addr(domain, sub || domain.startsWith("www.") ? domain.split(".")[0] : "@"),
    ...(sub ? { alternative: [{ name: domain, host: domain.split(".")[0], type: "CNAME", value: `${BOX}.` }] } : {}),
    found: [],
    url: `https://${domain}`,
    checkedAt: ago(8),
    nextCheckAt: soon(20),
    summary: "",
    ...extra,
  };
}

const mixed = [
  dom("shop.com", "live", { wwwRedirect: true, certificate: cert("shop.com") }),
  dom("www.shop.com", "live", { redirectTo: "shop.com", routes: [] }),
  dom("shopdemo.dev", "waiting_for_dns", { reason: "no A or AAAA record yet" }),
  dom("docs.shop.com", "issuing", { routes: [{ app: "docs", path: "/" }] }),
  dom("beta.shop.com", "error", {
    reason: "the certificate authority could not reach this box on ports 80 and 443; make sure both are open in the server's firewall: connection refused",
    routes: [
      { app: "web", path: "/" },
      { app: "docs", path: "/help" },
    ],
  }),
];

const managed = [dom("shop.com", "waiting_for_dns", { reason: "it points to 104.21.3.9, not this box (203.0.113.7)", managedBy: "cloudflare", found: ["104.21.3.9"] })];

async function domains(p: Page, list: unknown[]) {
  let current = list;
  await p.route("**/v1/projects/shop/domains", async (r: Route) => {
    if (r.request().method() === "POST") {
      const body = r.request().postDataJSON() as { domain: string; confirm?: string };
      if (!body.confirm)
        return r.fulfill({ status: 428, contentType: "application/problem+json", json: { status: 428, code: "confirm_required", title: "Confirm", plan: { hash: "abc12345def", risk: "reversible", ops: [] } } });
      const added = dom(body.domain, "waiting_for_dns", { reason: "not checked yet", checkedAt: undefined });
      current = [...current, added];
      return r.fulfill({ json: { applied: true, plan: { hash: "abc12345def", risk: "reversible", ops: [] }, domain: added } });
    }
    return r.fulfill({ json: current });
  });
  await p.route("**/v1/projects/shop/domains/*/check", (r) => {
    const d = decodeURIComponent(r.request().url().split("/domains/")[1].split("/")[0]);
    return r.fulfill({ json: (current as Array<{ domain: string }>).find((x) => x.domain === d) ?? {} });
  });
}

const sslip = {
  domain: BOX,
  source: "sslip",
  default: BOX,
  dashboard: `dashboard.${BOX}`,
  dashboardUrl: `https://dashboard.${BOX}`,
  dashboardCertificate: cert(`dashboard.${BOX}`),
  bare: [{ host: BOX, redirectsTo: `https://dashboard.${BOX}/` }],
  certificates: "acme",
  state: "live",
  publicIps: [IP4, IP6],
  summary: "",
};
const own = {
  ...sslip,
  domain: "example.com",
  source: "set",
  dashboard: "dashboard.example.com",
  dashboardUrl: "https://dashboard.example.com",
  dashboardCertificate: cert("dashboard.example.com"),
  bare: [{ host: "example.com", project: "shop" }],
  wildcard: { provider: "cloudflare", certificate: cert("*.example.com") },
  previous: { domain: BOX, until: soon(2400) },
  records: [...addr("example.com", "@"), ...addr("*.example.com", "*")].map((r) => ({ ...r, ok: true, found: [r.value] })),
};
const local = {
  domain: "tiffin.localhost",
  source: "flag",
  default: "tiffin.localhost",
  dashboard: "dashboard.tiffin.localhost",
  dashboardUrl: "https://dashboard.tiffin.localhost:8470",
  dashboardCertificate: { host: "dashboard.tiffin.localhost", state: "", checkedAt: ago(1) },
  certificates: "internal",
  state: "internal",
  publicIps: [],
  summary: "",
};

async function boxDomain(p: Page, b: unknown, check?: { ok: boolean; managedBy?: string }) {
  await p.route("**/v1/domain", (r) => r.fulfill({ json: b }));
  if (check)
    await p.route("**/v1/domain/check?*", (r) => {
      const domain = new URL(r.request().url()).searchParams.get("domain")!;
      const recs = [...addr(domain, "@"), ...addr(`*.${domain}`, "*")].map((x, i) => ({
        ...x,
        ok: check.ok || i === 0,
        found: check.ok || i === 0 ? [x.value] : [],
        ...(check.ok || i === 0 ? {} : { reason: "no A or AAAA record yet" }),
      }));
      return r.fulfill({ json: { domain, ok: check.ok, records: recs, managedBy: check.managedBy, summary: "" } });
    });
}

const cfZones = { name: "cloudflare", label: "Cloudflare", zones: ["example.com", "shopdemo.dev", "bilal.dev"], boxDomain: false, connectedAt: ago(3 * 86400), connectedBy: "Bilal" };
async function dns(p: Page, connected: boolean) {
  await p.route("**/v1/dns/providers", (r) => r.fulfill({ json: { connected: connected ? [cfZones] : [], available: [], summary: "" } }));
}

const scenes: Array<{ name: string; url: string; stub: (p: Page) => Promise<unknown>; wait: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown> }> = [
  {
    name: "project-domains",
    url: "/projects/shop/domains",
    stub: async (p) => (await domains(p, mixed), await boxDomain(p, sslip)),
    wait: (p) => p.getByText("Getting a certificate").filter({ visible: true }).first().waitFor(),
  },
  {
    name: "project-domains-hosts",
    url: "/projects/shop/domains",
    stub: async (p) => (await domains(p, mixed), await boxDomain(p, sslip)),
    wait: (p) => p.getByText("Waiting for DNS").filter({ visible: true }).first().waitFor(),
    act: (p) => p.getByRole("button", { name: "Namecheap" }).click(),
  },
  {
    name: "project-domains-empty",
    url: "/projects/shop/domains",
    stub: async (p) => (await domains(p, []), await boxDomain(p, sslip)),
    wait: (p) => p.getByText("None yet.").waitFor(),
    act: (p) => p.getByRole("textbox", { name: "Add a domain" }).fill("shop.com"),
  },
  {
    name: "project-domains-added",
    url: "/projects/shop/domains",
    stub: async (p) => (await domains(p, []), await boxDomain(p, sslip)),
    wait: (p) => p.getByText("None yet.").waitFor(),
    act: async (p) => {
      await p.getByRole("textbox", { name: "Add a domain" }).fill("shop.com");
      await p.getByRole("button", { name: "Add", exact: true }).click();
      await p.getByText("Add these records at your DNS host").waitFor();
    },
  },
  {
    name: "project-domains-managed",
    url: "/projects/shop/domains",
    stub: async (p) => (await domains(p, managed), await boxDomain(p, sslip)),
    wait: (p) => p.getByRole("button", { name: "Add the records for me" }).waitFor(),
  },
  {
    name: "project-domains-local",
    url: "/projects/shop/domains",
    stub: async (p) => (await domains(p, [dom("shop.com", "waiting_for_dns", { reason: "this box has no public IP address (a local box); custom domains need a server", records: [] })]), await boxDomain(p, local)),
    wait: (p) => p.getByText("Waiting for DNS").filter({ visible: true }).first().waitFor(),
  },
  {
    name: "project-overview",
    url: "/projects/shop",
    stub: (p) => domains(p, mixed),
    wait: (p) => p.getByRole("heading", { name: "Domains" }).waitFor(),
  },
  {
    name: "project-settings",
    url: "/projects/shop/settings",
    stub: (p) => domains(p, mixed),
    wait: (p) => p.getByText("domains of your own").waitFor(),
  },
  {
    name: "box-domain-automatic",
    url: "/settings",
    stub: (p) => boxDomain(p, sslip),
    wait: (p) => p.getByRole("button", { name: "Use your own domain" }).waitFor(),
  },
  {
    name: "box-domain-checking",
    url: "/settings",
    stub: (p) => boxDomain(p, sslip, { ok: false }),
    wait: (p) => p.getByRole("button", { name: "Use your own domain" }).waitFor(),
    act: async (p) => {
      await p.getByRole("button", { name: "Use your own domain" }).click();
      await p.getByLabel("Your own domain").fill("example.com");
      await p.getByRole("button", { name: "Check" }).click();
      await p.getByText("Not yet").filter({ visible: true }).first().waitFor();
      await p.locator("#domain").scrollIntoViewIfNeeded();
    },
  },
  {
    name: "box-domain-ready",
    url: "/settings",
    stub: (p) => boxDomain(p, sslip, { ok: true }),
    wait: (p) => p.getByRole("button", { name: "Use your own domain" }).waitFor(),
    act: async (p) => {
      await p.getByRole("button", { name: "Use your own domain" }).click();
      await p.getByLabel("Your own domain").fill("example.com");
      await p.getByRole("button", { name: "Check" }).click();
      await p.getByRole("button", { name: "Switch to example.com" }).waitFor();
    },
  },
  {
    name: "box-domain-own",
    url: "/settings",
    stub: (p) => boxDomain(p, own),
    wait: (p) => p.getByRole("button", { name: "Go back to the automatic address" }).waitFor(),
  },
  {
    name: "box-domain-local",
    url: "/settings",
    stub: (p) => boxDomain(p, local),
    wait: (p) => p.getByText("On this computer").waitFor(),
  },
  {
    name: "dns-not-connected",
    url: "/settings/dns",
    stub: (p) => dns(p, false),
    wait: (p) => p.getByLabel("Paste the token here.").waitFor(),
  },
  {
    name: "dns-connected",
    url: "/settings/dns",
    stub: (p) => dns(p, true),
    wait: (p) => p.getByText("Connected to Cloudflare.").waitFor(),
  },
];

for (const [label, size, scheme] of [
  ["desktop", { width: 1440, height: 1000 }, "light"],
  ["phone", { width: 390, height: 844 }, "light"],
  ["desktop-dark", { width: 1440, height: 1000 }, "dark"],
  ["phone-dark", { width: 390, height: 844 }, "dark"],
] as const) {
  test(`domains screens ${label}`, async ({ page, baseURL }) => {
    test.setTimeout(240_000);
    mkdirSync(out, { recursive: true });
    await page.setViewportSize(size);
    await page.emulateMedia({ colorScheme: scheme });
    await signIn(page, baseURL!);
    for (const s of scenes) {
      if (only && !only.includes(s.name)) continue;
      const p = await page.context().newPage();
      await p.setViewportSize(size);
      await p.emulateMedia({ colorScheme: scheme });
      await s.stub(p);
      await p.goto(s.url);
      await s.wait(p);
      if (s.act) await s.act(p);
      await shot(p, `${label}-${s.name}`);
      await p.close();
    }
  });
}
