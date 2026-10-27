# Search engines

An app gets found on a domain of its own. Once a production app is live on your own
domain, the box tells search engines about its pages, checks what they will see, and
shows which search and AI bots actually came. Nothing to set up.

## Which addresses are findable

| Address | Search engines |
|---|---|
| Your own domain on a production app (`example.com`, `shop.example.com`) | Findable |
| A name on `shiptiffin.app` (managed boxes and `<project>.shiptiffin.app`), chosen names included | Hidden |
| The automatic `sslip.io` name, local boxes | Hidden |
| Previews and version addresses | Hidden |
| A domain that redirects (www, an old primary domain) | Search engines see where it leads |

Hidden addresses send `X-Robots-Tag: noindex` on every response, so search engines
leave them out. A shared domain shares its reputation: one project's spam would weigh
on every other, so names there are never indexed. To be found, [add a domain of your
own](https://shiptiffin.com/docs/domains.md#a-domain-for-one-project), or [buy one](https://shiptiffin.com/docs/domains.md#buying-a-domain).
On a self-hosted box whose domain you set with `tiffin domain set`, the box's names
(`shop.example.com`) are your own domain, and findable.

## Keeping an app out of search results

A staging site, an internal tool or a site before launch can stay out of Google and
Bing while it stays open for anyone with the link. In **Project › Settings › General ›
Search engines**, turn on **Keep out of search results** for the whole project, or use
**Keep out of search results** in one domain's ⋯ menu in Domains (a `staging.`
subdomain in a public project; `search set` replaces both settings at once). Those addresses then send `X-Robots-Tag: noindex`;
search engines drop pages they had within a few weeks; the box sends them no IndexNow
messages and verifies nothing with Google. It is not a lock: for that, put sign-in in
front of the app.

```bash
tiffin search set shop --hidden                                 # the whole project
tiffin search set shop --hidden-hosts staging.example.com      # one address
tiffin search set shop                                          # everything findable again
```

## What the box does

For each findable address it reads `robots.txt`, the home page and the sitemap, and
tells search engines which pages are new, changed or removed through
[IndexNow](https://www.indexnow.org). One message reaches Bing, Yandex, Seznam,
Naver, Yep, Amazon and the Internet Archive; DuckDuckGo, Yahoo and Copilot use Bing's
index. Google takes no pings: it reads your sitemap on its own.

| When | What is sent |
|---|---|
| The domain goes live (DNS and HTTPS ready) | Every page in the sitemap, once |
| A couple of minutes after a deploy | Pages that are new, removed, or whose `lastmod` changed. Several deploys in a row make one message. |
| Every 6 hours | The same comparison, for pages your database makes without a deploy |

The proof is a key file the box serves at `https://<domain>/<key>.txt` on every app
host; your app needs no change. Pages already sent are never sent again unless they
change: engines treat repeats as spam.

**The sitemap is what counts.** The box finds it through the `Sitemap:` line in
`robots.txt`, else at `/sitemap.xml` (sitemap indexes and `.gz` files work). Only pages
on the same domain are sent. Without a sitemap, only the home page is sent, once. Give
each page its real `lastmod`: when every page has the same date (usually the build
time, as many Next.js sitemaps do), the box can't tell which changed and sends only new
and removed pages.

In Next.js, `app/sitemap.ts`:

```ts
import type { MetadataRoute } from "next";

export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
  const posts = await db.posts.findMany({ select: { slug: true, updatedAt: true } });
  return [
    { url: "https://example.com/", lastModified: new Date("2026-10-01") },
    ...posts.map((p) => ({ url: `https://example.com/blog/${p.slug}`, lastModified: p.updatedAt })),
  ];
}
```

and `app/robots.ts` with `sitemap: "https://example.com/sitemap.xml"`.

## Which bots came

The box reads its own access log for the crawlers that matter: Googlebot, Bingbot,
Applebot and DuckDuckBot (search), OpenAI's OAI-SearchBot, Claude-SearchBot and
PerplexityBot (AI search), ChatGPT-User, Claude-User and Perplexity-User (a page
fetched for someone's question in a chat), and GPTBot and ClaudeBot (AI training).
A bot counts only when it comes from the addresses its company publishes; anything
else claiming to be Googlebot is counted apart, as a fake, and ignored. For each: when
it last came, which page, how many pages in the last 7 days and how many answered with
an error. The daily counts are kept for 16 months, for Analytics › Search.

These are visits, not rankings: Googlebot reading a page does not mean Google shows
it. For that, use Google Search Console.

## Google Search Console

Google reads your sitemap without being told. To see what people search for and which
pages Google indexed, connect Google once per box, in **Settings › Search engines**. It
takes about 5 minutes in your own Google Cloud account, nothing is billed, and the box
does the rest for every domain, now and later.

1. At [console.cloud.google.com](https://console.cloud.google.com/projectcreate),
   create a project (`tiffin`, say).
2. In it, turn on the **Google Search Console API** and the **Site Verification API**.
3. Under **IAM › Service accounts**, create a service account named `tiffin` with no
   roles. Open it, then **Keys › Add key › Create new key › JSON**: a file downloads.
4. In Settings › Search engines, choose that file, and enter the Google account you use
   for Search Console.

The service account is a robot Google account in your project; the box signs in as it
with the key (kept encrypted, never shown again). For each findable address it:

- **verifies the domain**: with Cloudflare connected, it adds Google's TXT record
  beside the domain's other records (SPF and the rest stay as they are), and the
  Domain property `sc-domain:example.com` covers every name under it. A domain whose
  DNS is elsewhere is verified with Google's verification file, which the box serves
  on the host; that property covers the one host. The record must stay: Google checks
  it again from time to time.
- **adds your Google account as an owner**, so the domain shows in your own Search
  Console, and submits the sitemap, once. Google reads a submitted sitemap again on its
  own; the box submits it again only when you press **Tell search engines** (or run
  `tiffin search ping`). It shows when Google last read it, how many pages it found and
  any errors.
- **reads web search every 12 hours**: clicks, impressions, average position, the top
  searches and pages, day by day. The first read goes back as far as Google keeps (16
  months) and the box keeps them as long, in `search/search.db` on the data disk; later
  reads take the last 10 days again, since Google revises its last few days. It also asks
  URL Inspection about about 20 pages a day, so "Google has indexed 118 of 124 pages"
  fills in over a week.

If Google later refuses the key (it was deleted, an API was turned off), Settings ›
Search engines and the box's status checks say so, and the domains show it too. Make a
new key for the same service account and choose **Replace key**: every domain stays
verified. A different service account starts over and verifies each domain again.

Without the connection, add the domain in Search Console yourself (**Domain**, then the
TXT record it shows); the box notices the record and shows the domain as verified.

An agent can connect it too: put the key file's text in a request body (never in chat
or the repo) and run `tiffin search google connect --body-file body.json`, where
`body.json` is `{"key": "<the file's contents>", "owner": "you@gmail.com"}`. To change only
the owner later, leave the key out: `tiffin search google connect --owner you@gmail.com`.

## In the dashboard and for agents

**Analytics › Search** in each project shows it for any days you choose, beside the
project's other analytics:

- **Searches**: Google's clicks, impressions, click rate and average position, each
  against the days before, a chart with the project's deploys marked, and the top
  searches and pages, and **what moved**: the searches and pages that gained or lost the
  most clicks (then impressions) against the days before, counting only changes bigger
  than chance. Periods are 7 days, 28 days (the default), 3 months (13 weeks) or 12
  months: whole weeks, so both periods hold the same weekdays. Google is still counting
  its last 2 to 3 days, so a period ends on the last day it has finished (it says which);
  comparing unfinished days would make every week look like a fall.
- **Found**: when the box last told IndexNow, how many pages the sitemap lists (and how
  many Google found in it), whether Google is connected and verified, how many of the
  pages checked are in Google's index, with Google's reason for each one that isn't, in
  plain words, and anything wrong with robots.txt, the home page or the sitemap.
- **Search engine crawlers**: which search engines read the pages, how often, when last,
  and their errors; requests that claimed to be one from elsewhere are counted apart.
- **AI**: visits from ChatGPT, Claude, Perplexity, Gemini and other AI apps (from the
  project's analytics), and the AI companies' bots: fetched for someone's chat, AI search
  or training.

**Settings › Search engines** lists every engine: IndexNow (on, with each findable
address and when it was last told) and the Google connection.

**Domains**: each live domain of your own has a **Search engines** section: findable or
not (and why), when the box last told search engines and about which pages, which bots
came, what it found in `robots.txt`, the home page and the sitemap, and, with Google
connected, its searches and index status. **Tell search engines** reads the sitemap at once, sends IndexNow what changed and,
with Google connected, submits the sitemap to Google again.

```bash
tiffin search status shop                      # every address: findable, last ping, bots, checks
tiffin search ping shop                        # check now; send what changed
tiffin search ping shop --host example.com --urls https://example.com/pricing
tiffin search ping shop --all                  # every page again (at most once an hour)
tiffin search inspect shop --page https://example.com/pricing   # Google: indexed or not, and why
tiffin search report shop --period 91d         # 7d, 28d (default), 91d or 12mo; or --from/--to
tiffin search report shop --from 2026-09-01 --to 2026-09-30 --host example.com
tiffin search indexnow                         # every findable address and its last ping
tiffin search google                           # the Google connection and its properties
```

An agent asked "why isn't my site on Google?" starts with `tiffin search status`:
a hidden address, a `Disallow: /` in robots.txt, a noindex home page or a missing
sitemap shows there in words. With Google connected it also shows the searches, the
pages Google has not indexed and why, and `search inspect` asks about any one page.
"Why did traffic drop?" starts with `search report`: `searches.moved` lists the searches
and pages that fell (and rose) against the days before; line them up with the deploys
around the drop.
