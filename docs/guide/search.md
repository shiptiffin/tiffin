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
own](domains.md#a-domain-for-one-project), or [buy one](domains.md#buying-a-domain).
On a self-hosted box whose domain you set with `tiffin domain set`, the box's names
(`shop.example.com`) are your own domain, and findable.

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
an error. It keeps 30 days.

These are visits, not rankings: Googlebot reading a page does not mean Google shows
it. For that, use Google Search Console.

## Google Search Console

Google reads your sitemap without being told. To see the searches people find you with
and which pages Google indexed, add the domain to
[Search Console](https://search.google.com/search-console) once: choose **Domain**,
enter `example.com`, and add the TXT record it shows (with Cloudflare connected, add
it in **Domains › example.com › DNS records**). It covers every name under the
domain. The box notices the record and shows the domain as verified.

## In the dashboard and for agents

**Domains**: each live domain of your own has a **Search engines** section: findable or
not (and why), when the box last told search engines and about which pages, which bots
came, what it found in `robots.txt`, the home page and the sitemap, and whether Search
Console is verified. **Check now** reads the sitemap at once. **Settings › General ›
Search engines** turns the IndexNow messages off for a project (the checks go on).

```bash
tiffin search status shop                      # every address: findable, last ping, bots, checks
tiffin search ping shop                        # check now; send what changed
tiffin search ping shop --host example.com --urls https://example.com/pricing
tiffin search ping shop --all                  # every page again (at most once an hour)
tiffin search set shop --index-now=false       # stop the pings for this project
```

An agent asked "why isn't my site on Google?" starts with `tiffin search status`:
a hidden address, a `Disallow: /` in robots.txt, a noindex home page or a missing
sitemap shows there in words.
