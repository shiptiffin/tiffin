import type { components } from "../../src/api/schema";

type Report = components["schemas"]["SearchReport"];

const DAY = 86_400_000;
const iso = (t: number) => new Date(t).toISOString().slice(0, 10);

/**
 * A young site's search report, shaped like the box's: Google's numbers end
 * three days ago, a slow climb from zero, a few searches and pages, a third
 * of the pages indexed, and the crawlers of the last 28 days. With quiet,
 * nothing moved beyond chance. state "none"
 * gives the report of a project with nothing findable; google
 * "not_connected" one with no Google connection.
 */
export function searchReport(project: string, opts: { state?: "verified" | "not_connected" | "none"; days?: number; quiet?: boolean } = {}): Report {
  const state = opts.state ?? "verified";
  const n = opts.days ?? 28;
  const now = Date.now();
  const to = now - 3 * DAY;
  const from = to - (n - 1) * DAY;
  const daily = Array.from({ length: n }, (_, i) => {
    const impressions = Math.round(4 + i * 1.6 + (i % 7 === 5 ? 9 : 0));
    return { date: iso(from + i * DAY), clicks: Math.round(impressions * 0.06), impressions, position: Math.round((22 - i * 0.35) * 10) / 10 };
  });
  const prevDaily = daily.map((d, i) => ({
    ...d,
    date: iso(from - n * DAY + i * DAY),
    impressions: Math.round(d.impressions * 0.55),
    clicks: Math.round(d.clicks * 0.5),
    position: d.position + 4,
  }));
  const sum = (ds: typeof daily) => {
    const c = ds.reduce((a, d) => a + d.clicks, 0);
    const im = ds.reduce((a, d) => a + d.impressions, 0);
    const p = ds.reduce((a, d) => a + d.position * d.impressions, 0);
    return { clicks: c, impressions: im, ctr: im ? Math.round((c / im) * 1000) / 1000 : 0, position: im ? Math.round((p / im) * 10) / 10 : 0 };
  };
  const host = `${project}.example.com`;
  const findable = state !== "none";
  return {
    project,
    from: iso(from),
    to: iso(to),
    prevFrom: iso(from - n * DAY),
    prevTo: iso(from - DAY),
    crawlFrom: iso(now - (n - 1) * DAY),
    crawlTo: iso(now),
    hosts: findable
      ? [
          { host, app: "web", findable: true },
          {
            host: `${project}.shiptiffin.app`,
            app: "web",
            findable: false,
            why: "Addresses on shiptiffin.app are hidden from search engines.",
            fix: "Add a domain of your own to be found.",
          },
        ]
      : [
          {
            host: `${project}.shiptiffin.app`,
            app: "web",
            findable: false,
            why: "Addresses on shiptiffin.app are hidden from search engines.",
            fix: "Add a domain of your own to be found.",
          },
        ],
    indexNow: findable
      ? {
          on: true,
          pages: 36,
          checked: new Date(now - 40 * 60_000).toISOString(),
          last: { at: new Date(now - 3 * 3_600_000).toISOString(), reason: "deploy", pages: 4, ok: true, status: 200 },
          problems: ["Sitemap: 3 pages share one lastmod, so changes to them can't be told apart."],
        }
      : { on: false, pages: 0 },
    google:
      state === "verified"
        ? {
            state: "verified",
            property: `sc-domain:${project}.example.com`,
            pulledAt: new Date(now - 2 * 3_600_000).toISOString(),
            first: iso(now - 200 * DAY),
            sitemap: { url: `https://${host}/sitemap.xml`, pages: 34, errors: 0, warnings: 0, lastRead: new Date(now - DAY).toISOString() },
            index: {
              checked: 36,
              indexed: 12,
              notIndexed: [
                { url: `https://${host}/compare/coolify`, coverage: "Discovered - currently not indexed", at: new Date(now - DAY).toISOString() },
                { url: `https://${host}/docs/limits`, coverage: "Crawled - currently not indexed", at: new Date(now - DAY).toISOString() },
                { url: `https://${host}/agents`, coverage: "Discovered - currently not indexed", at: new Date(now - 2 * DAY).toISOString() },
                {
                  url: `https://${host}/pricing?plan=team`,
                  coverage: "Duplicate without user-selected canonical",
                  at: new Date(now - 2 * DAY).toISOString(),
                },
                { url: `https://${host}/changelog`, coverage: "URL is unknown to Google", at: new Date(now - 3 * DAY).toISOString() },
              ],
            },
          }
        : { state: state === "none" ? "none" : "not_connected" },
    searches:
      state === "verified"
        ? {
            totals: sum(daily),
            previous: sum(prevDaily),
            daily,
            prevDaily,
            through: iso(to + 2 * DAY),
            complete: iso(to),
            queries: [
              ["self hosted vercel alternative", 14, 210, 8.2],
              ["deploy next.js on hetzner", 9, 160, 6.4],
              ["one server for all my apps", 5, 88, 11.3],
              ["coolify vs dokku", 3, 140, 14.8],
              ["hetzner ipv6", 1, 61, 19.5],
              ["postgres and redis on one vps", 1, 40, 12.1],
              ["indexnow next.js", 0, 35, 9.9],
              ["tiffin app box", 0, 22, 3.1],
              ["cheap paas for side projects", 0, 18, 27.4],
              ["vercel bill too high", 0, 15, 31.2],
              ["passkey only admin dashboard", 0, 9, 7.7],
              ["better auth self hosted", 0, 7, 24],
            ].map(([key, clicks, impressions, position]) => ({
              key: key as string,
              clicks: clicks as number,
              impressions: impressions as number,
              position: position as number,
            })),
            pages: [
              ["/", 21, 420, 9.1],
              ["/compare/vercel", 8, 260, 7.9],
              ["/docs/quickstart", 4, 130, 6.2],
              ["/docs/limits", 0, 95, 18.4],
              ["/security", 0, 40, 22.5],
              ["/privacy", 0, 6, 41],
            ].map(([key, clicks, impressions, position]) => ({
              key: key as string,
              clicks: clicks as number,
              impressions: impressions as number,
              position: position as number,
            })),
            moved: opts.quiet
              ? { queriesUp: [], queriesDown: [], pagesUp: [], pagesDown: [] }
              : {
                  queriesUp: [
                    mover("self hosted vercel alternative", 14, 6, 210, 120, 8.2, 11.4),
                    mover("deploy next.js on hetzner", 9, 3, 160, 90, 6.4, 9.8),
                    mover("coolify vs dokku", 3, 0, 140, 0, 14.8, 0),
                    mover("indexnow next.js", 0, 0, 35, 12, 9.9, 15.2),
                  ],
                  queriesDown: [mover("heroku alternative", 0, 4, 12, 95, 38.1, 12.6), mover("vercel bill too high", 0, 0, 15, 40, 31.2, 22)],
                  pagesUp: [mover("/", 21, 11, 420, 230, 9.1, 13), mover("/compare/vercel", 8, 2, 260, 80, 7.9, 12.2)],
                  pagesDown: [mover("/blog/launch", 0, 3, 0, 70, 0, 9.4)],
                },
          }
        : undefined,
    crawlers: findable
      ? [
          {
            bot: "Googlebot",
            operator: "Google",
            purpose: "search",
            ai: false,
            hits: 412,
            errors: 6,
            lastAt: new Date(now - 25 * 60_000).toISOString(),
            lastPath: "/docs/limits",
            lastStatus: 200,
          },
          {
            bot: "ClaudeBot",
            operator: "Anthropic",
            purpose: "training",
            ai: true,
            hits: 96,
            errors: 0,
            lastAt: new Date(now - 5 * 3_600_000).toISOString(),
            lastPath: "/",
            lastStatus: 200,
          },
          {
            bot: "Bingbot",
            operator: "Bing",
            purpose: "search",
            ai: false,
            hits: 58,
            errors: 0,
            lastAt: new Date(now - 2 * 3_600_000).toISOString(),
            lastPath: "/pricing",
            lastStatus: 200,
          },
          {
            bot: "OAI-SearchBot",
            operator: "OpenAI",
            purpose: "search",
            ai: true,
            hits: 31,
            errors: 0,
            lastAt: new Date(now - 9 * 3_600_000).toISOString(),
            lastPath: "/compare/vercel",
            lastStatus: 200,
          },
          {
            bot: "ChatGPT-User",
            operator: "OpenAI",
            purpose: "user",
            ai: true,
            hits: 12,
            errors: 0,
            lastAt: new Date(now - DAY).toISOString(),
            lastPath: "/docs/quickstart",
            lastStatus: 200,
          },
          {
            bot: "DuckDuckBot",
            operator: "DuckDuckGo",
            purpose: "search",
            ai: false,
            hits: 9,
            errors: 1,
            lastAt: new Date(now - 3 * DAY).toISOString(),
            lastPath: "/old",
            lastStatus: 404,
          },
          {
            bot: "Applebot",
            operator: "Apple",
            purpose: "search",
            ai: false,
            hits: 4,
            errors: 0,
            lastAt: new Date(now - 6 * DAY).toISOString(),
            lastPath: "/",
            lastStatus: 200,
          },
        ]
      : [],
    fakes: findable ? 7 : 0,
  } as Report;
}

function mover(
  key: string,
  clicks: number,
  prevClicks: number,
  impressions: number,
  prevImpressions: number,
  position: number,
  prevPosition: number,
) {
  return { key, clicks, prevClicks, impressions, prevImpressions, position, prevPosition };
}

export function indexNow() {
  const now = Date.now();
  return {
    on: true,
    addresses: [
      {
        host: "reelbird.app",
        project: "reelbird",
        last: { at: new Date(now - 26 * 3_600_000).toISOString(), reason: "live", pages: 26, ok: true, status: 200 },
      },
      {
        host: "shiptiffin.com",
        project: "website",
        last: { at: new Date(now - 40 * 60_000).toISOString(), reason: "deploy", pages: 1, ok: true, status: 200 },
      },
      { host: "watch.reelbird.app", project: "reelbird" },
    ],
  };
}
