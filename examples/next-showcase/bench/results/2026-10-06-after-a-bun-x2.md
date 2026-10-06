# Showcase bench: after-a-bun-x2

2026-10-06 03:14 UTC. Target https://next-showcase.tiffin.localhost:54686, load via the VM's address 192.168.64.4, VM tiffin-bench-1791254123.

- runtime: bun
- instances: 2
- deploy: dep_01M47K928G55RN7MJAT5ZZW6Z3
- tiffin: {"commit":"9f6d584+","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Tiffin after the fixes: Next.js on Bun as one process (exec bun --bun ./node_modules/next/dist/bin/next start), 2 instances, Valkey cache handlers, build with the read-only service env, prerenders served from the build, box-precompressed client files. Per-IP app rate limit raised to 1,000,000/10 s for the load generator.

## Functional checks: 23/23 pass

|  | Check | Result | Details |
|---|---|---|---|
| F01 | home: static, proxy header, compressed | pass | cache-control=s-maxage=31536000 x-nextjs-cache=HIT x-nextjs-prerender=1, 1 encoding=zstd 18435→4634 B (20 ms) |
| F02 | next/font: woff2 preloaded and served immutable | pass | Geist_Variable-s.p.03oqkqkevsckn.woff2?dpl=dep_01M47K928G55RN7MJAT5ZZW6Z3 font/woff2 public, max-age=31536000, immutable (9 ms) |
| F03 | client chunk: immutable, compressed by the edge | pass | cache-control=public, max-age=31536000, immutable content-encoding=zstd (8 ms) |
| F04 | next/image local: every srcset candidate loads | pass | 8 widths, image/webp, browser picked 1080w (134 ms) |
| F05 | next/image remote: the bucket's featured image loads | pass | https://files.tiffin.localhost:54686/next-showcase/media/featured.jpg → 456px (2 ms) |
| F06 | Open Graph: og:image is an absolute URL on this site | pass | https://next-showcase.tiffin.localhost:54686/opengraph-image?3f305df0986b521c (0 ms) |
| F07 | Open Graph image decodes (1200×630 PNG) | pass | 71533 B, cache-control=public, max-age=0, must-revalidate (20 ms) |
| F08 | client navigation (RSC) from / to /products | pass | soft navigation (65 ms) |
| F09 | blog: prerendered at build, served from cache (ISR) | pass | cache-control=s-maxage=60, stale-while-revalidate=3540 x-nextjs-cache=HIT x-nextjs-prerender=1, 1, first copy rendered 14 s ago (12 ms) |
| F10 | proxy: /dashboard without a session redirects to /login on this site | pass | 307 → /login (5 ms) |
| F11 | login: form Server Action sets the session, redirects to /dashboard | pass | cookie secure=false httpOnly=true (199 ms) |
| F12 | streaming: dashboard shell arrives before the slow section (edge, browser encoding) | pass | zstd: shell 13 ms, slow section 1056 ms, 13 chunks; uncompressed: shell 9 ms (2096 ms) |
| F13 | streaming in Chromium: shell painted before the slow section | pass | shell 63 ms, slow section 1351 ms (1382 ms) |
| F14 | Server Action: useActionState + useOptimistic note, then saved | pass | optimistic 29 ms, saved 818 ms (400 ms of it is the action's own wait), kept after reload (1642 ms) |
| F15 | Server Action: useOptimistic like, round trip | pass | optimistic 17 ms, server round trip 30 ms (38 ms) |
| F16 | Server Action over fetch (Next-Action header, as in the load test) | pass | 200 45 B in 8 ms (8 ms) |
| F17 | ISR on demand: revalidatePath from the dashboard reaches every instance | pass | new revision everywhere after 116 ms; 1 distinct render time(s) across instances (136 ms) |
| F18 | use cache + updateTag: restock refreshes /products everywhere, one shared entry | pass | 1 shared entry before and after (256 ms) |
| F19 | PPR: /products static shell first, cart count streamed from the cookie | pass | grid at 7 ms of 10 ms; add to cart: "Cart: 0" → "Cart: 1" (202 ms) |
| F20 | route handler /api/items: JSON from Postgres on every instance | pass | 12 items, instances 0,1, encoding=none (58 ms) |
| F21 | SSE /api/stream: events arrive as sent (browser encoding) | pass | zstd: 10 events, first 6 ms, last 1819 ms; identity: first 7 ms (3638 ms) |
| F22 | next/image: a new image is optimized, then served from the image cache | pass | cold 79 ms (image/webp, 24212 B), warm 4 ms HIT (84 ms) |
| F23 | no console errors, failed requests or 4xx/5xx in the browser | pass | clean (0 ms) |

## Lighthouse (median of 5 runs)

| Page | Form factor | Score | FCP ms | LCP ms | TBT ms | CLS | SI ms | TTFB ms | KB |
|---|---|---|---|---|---|---|---|---|---|
| / | mobile | 99 | 762 | 2129 | 7 | 0.000 | 762 | 6 | 262 |
| / | desktop | 100 | 205 | 470 | 0 | 0.000 | 205 | 4 | 284 |
| /blog/hello-box | mobile | 100 | 761 | 1862 | 10 | 0.000 | 761 | 6 | 208 |
| /blog/hello-box | desktop | 100 | 205 | 420 | 0 | 0.000 | 205 | 8 | 208 |
| /products | mobile | 100 | 763 | 1861 | 9 | 0.000 | 763 | 8 | 209 |
| /products | desktop | 100 | 204 | 417 | 0 | 0.000 | 204 | 4 | 209 |
| /dashboard | mobile | 80 | 762 | 1803 | 0 | 0.432 | 762 | 4 | 211 |
| /dashboard | desktop | 100 | 204 | 402 | 0 | 0.045 | 204 | 4 | 211 |

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 999.9 | 100 | 0.4 | 0.7 | 1.8 | 18.7 | - |
| home (static) | vzNAT | 400 | 400 | 100 | 1.5 | 9.4 | 28.5 | 83.4 | - |
| home (static) | forwarder | 400 | 400 | 100 | 1.7 | 10 | 32.1 | 99.9 | - |
| blog (ISR) | vzNAT | 400 | 400 | 100 | 1.3 | 2.9 | 7.1 | 38.1 | - |
| products (PPR) | vzNAT | 200 | 200 | 100 | 4.3 | 9 | 29.4 | 112.1 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 5.8 | 15.1 | 46.3 | 84.2 | - |
| api/items (JSON) | vzNAT | 400 | 400 | 100 | 1.3 | 15.1 | 96.9 | 324.9 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 1.1 | 2.9 | 18.7 | 128.7 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 29.6 | 38.4 | 46.7 | 50.4 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 2.5 | 4.4 | 5.5 | 19.3 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | bun | 79 | 122 | 223 | 214 | 270 | 236 |
| 1 | bun | 76 | 123 | 240 | 232 | 286 | 264 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1963 | 1933 | 329 | 13 | 208 | 523 | 51 |
| after load | 2014 | 1882 | 401 | 13 | 199 | 352 | 50 |

