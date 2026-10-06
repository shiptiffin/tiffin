# Showcase bench: after-c-bun-x1

2026-10-06 03:53 UTC. Target https://next-showcase.tiffin.localhost:54686, load via the VM's address 192.168.64.4, VM tiffin-bench-1791254123.

- runtime: bun
- instances: 1
- deploy: dep_01M47N5HJ8MBWQ4W1Q7JZWE3GF
- tiffin: {"commit":"9f6d584+","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Bun, one instance, after the fixes (no Lighthouse in this run). Per-IP app rate limit raised to 1,000,000/10 s for the load generator.

## Functional checks: 23/23 pass

|  | Check | Result | Details |
|---|---|---|---|
| F01 | home: static, proxy header, compressed | pass | cache-control=s-maxage=31536000 x-nextjs-cache=HIT x-nextjs-prerender=1, 1 encoding=zstd 18435→4607 B (9 ms) |
| F02 | next/font: woff2 preloaded and served immutable | pass | Geist_Variable-s.p.03oqkqkevsckn.woff2?dpl=dep_01M47N5HJ8MBWQ4W1Q7JZWE3GF font/woff2 public, max-age=31536000, immutable (15 ms) |
| F03 | client chunk: immutable, compressed by the edge | pass | cache-control=public, max-age=31536000, immutable content-encoding=zstd (7 ms) |
| F04 | next/image local: every srcset candidate loads | pass | 8 widths, image/webp, browser picked 1080w (70 ms) |
| F05 | next/image remote: the bucket's featured image loads | pass | https://files.tiffin.localhost:54686/next-showcase/media/featured.jpg → 456px (2 ms) |
| F06 | Open Graph: og:image is an absolute URL on this site | pass | https://next-showcase.tiffin.localhost:54686/opengraph-image?3f305df0986b521c (0 ms) |
| F07 | Open Graph image decodes (1200×630 PNG) | pass | 71533 B, cache-control=public, max-age=0, must-revalidate (22 ms) |
| F08 | client navigation (RSC) from / to /products | pass | soft navigation (73 ms) |
| F09 | blog: prerendered at build, served from cache (ISR) | pass | cache-control=s-maxage=60, stale-while-revalidate=3540 x-nextjs-cache=STALE x-nextjs-prerender=1, 1, first copy rendered 340 s ago (1019 ms) |
| F10 | proxy: /dashboard without a session redirects to /login on this site | pass | 307 → /login (4 ms) |
| F11 | login: form Server Action sets the session, redirects to /dashboard | pass | cookie secure=false httpOnly=true (185 ms) |
| F12 | streaming: dashboard shell arrives before the slow section (edge, browser encoding) | pass | zstd: shell 10 ms, slow section 1028 ms, 12 chunks; uncompressed: shell 8 ms (2046 ms) |
| F13 | streaming in Chromium: shell painted before the slow section | pass | shell 57 ms, slow section 1341 ms (1370 ms) |
| F14 | Server Action: useActionState + useOptimistic note, then saved | pass | optimistic 37 ms, saved 826 ms (400 ms of it is the action's own wait), kept after reload (1657 ms) |
| F15 | Server Action: useOptimistic like, round trip | pass | optimistic 25 ms, server round trip 43 ms (50 ms) |
| F16 | Server Action over fetch (Next-Action header, as in the load test) | pass | 200 46 B in 10 ms (10 ms) |
| F17 | ISR on demand: revalidatePath from the dashboard reaches every instance | pass | new revision everywhere after 101 ms; 1 distinct render time(s) across instances (124 ms) |
| F18 | use cache + updateTag: restock refreshes /products everywhere, one shared entry | pass | 1 shared entry before and after (174 ms) |
| F19 | PPR: /products static shell first, cart count streamed from the cookie | pass | grid at 6 ms of 8 ms; add to cart: "Cart: 0" → "Cart: 1" (177 ms) |
| F20 | route handler /api/items: JSON from Postgres on every instance | pass | 12 items, instances 0, encoding=none (34 ms) |
| F21 | SSE /api/stream: events arrive as sent (browser encoding) | pass | zstd: 10 events, first 6 ms, last 1824 ms; identity: first 7 ms (3644 ms) |
| F22 | next/image: a new image is optimized, then served from the image cache | pass | cold 105 ms (image/webp, 24212 B), warm 5 ms HIT (110 ms) |
| F23 | no console errors, failed requests or 4xx/5xx in the browser | pass | clean (0 ms) |

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 1000 | 100 | 0.4 | 1.3 | 5.7 | 20.4 | - |
| home (static) | vzNAT | 400 | 400 | 100 | 1.3 | 2.6 | 5 | 24.4 | - |
| home (static) | forwarder | 400 | 399.9 | 100 | 1.5 | 14 | 51.8 | 145 | - |
| blog (ISR) | vzNAT | 400 | 400 | 100 | 2.7 | 149 | 264.3 | 496.9 | - |
| products (PPR) | vzNAT | 200 | 200 | 100 | 3.7 | 7.9 | 18.8 | 122.6 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 5.3 | 9.1 | 19.3 | 53.3 | - |
| api/items (JSON) | vzNAT | 400 | 400 | 100 | 1.1 | 2.3 | 4.2 | 17.8 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 0.9 | 1.7 | 3.7 | 14.6 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 28.9 | 35.2 | 40.4 | 54.9 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 2.6 | 5.5 | 8.8 | 24.8 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | bun | 153 | 107 | 394 | 378 | 279 | 401 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1596 | 2300 | 342 | 12 | 168 | 392 | 48 |
| after load | 1833 | 2063 | 408 | 12 | 220 | 392 | 49 |

