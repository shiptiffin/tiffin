# Showcase bench: after-b-node-x2

2026-10-06 03:27 UTC. Target https://next-showcase.tiffin.localhost:54686, load via the VM's address 192.168.64.4, VM tiffin-bench-1791254123.

- runtime: node
- instances: 2
- deploy: dep_01M47M0RKA5XE033S2496KD95F
- tiffin: {"commit":"9f6d584+","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Same app and box; command: node node_modules/next/dist/bin/next start (Node.js 24 via RAILPACK_PACKAGES), started with exec. tiffin-sdk's cache handlers share Valkey on Node too (its own RESP client). Per-IP app rate limit raised to 1,000,000/10 s for the load generator.

## Functional checks: 23/23 pass

|  | Check | Result | Details |
|---|---|---|---|
| F01 | home: static, proxy header, compressed | pass | cache-control=s-maxage=31536000 x-nextjs-cache=HIT x-nextjs-prerender=1, 1 encoding=zstd 18435→4638 B (21 ms) |
| F02 | next/font: woff2 preloaded and served immutable | pass | Geist_Variable-s.p.03oqkqkevsckn.woff2?dpl=dep_01M47M0RKA5XE033S2496KD95F font/woff2 public, max-age=31536000, immutable (248 ms) |
| F03 | client chunk: immutable, compressed by the edge | pass | cache-control=public, max-age=31536000, immutable content-encoding=zstd (7 ms) |
| F04 | next/image local: every srcset candidate loads | pass | 8 widths, image/webp, browser picked 1080w (338 ms) |
| F05 | next/image remote: the bucket's featured image loads | pass | https://files.tiffin.localhost:54686/next-showcase/media/featured.jpg → 456px (2 ms) |
| F06 | Open Graph: og:image is an absolute URL on this site | pass | https://next-showcase.tiffin.localhost:54686/opengraph-image?3f305df0986b521c (0 ms) |
| F07 | Open Graph image decodes (1200×630 PNG) | pass | 71533 B, cache-control=public, max-age=0, must-revalidate (27 ms) |
| F08 | client navigation (RSC) from / to /products | pass | soft navigation (77 ms) |
| F09 | blog: prerendered at build, served from cache (ISR) | pass | cache-control=s-maxage=60, stale-while-revalidate=3540 x-nextjs-cache=HIT x-nextjs-prerender=1, 1, first copy rendered 18 s ago (13 ms) |
| F10 | proxy: /dashboard without a session redirects to /login on this site | pass | 307 → /login (5 ms) |
| F11 | login: form Server Action sets the session, redirects to /dashboard | pass | cookie secure=false httpOnly=true (195 ms) |
| F12 | streaming: dashboard shell arrives before the slow section (edge, browser encoding) | pass | zstd: shell 32 ms, slow section 1089 ms, 11 chunks; uncompressed: shell 11 ms (2127 ms) |
| F13 | streaming in Chromium: shell painted before the slow section | pass | shell 73 ms, slow section 1365 ms (1404 ms) |
| F14 | Server Action: useActionState + useOptimistic note, then saved | pass | optimistic 36 ms, saved 824 ms (400 ms of it is the action's own wait), kept after reload (870 ms) |
| F15 | Server Action: useOptimistic like, round trip | pass | optimistic 40 ms, server round trip 59 ms (67 ms) |
| F16 | Server Action over fetch (Next-Action header, as in the load test) | pass | 200 46 B in 29 ms (29 ms) |
| F17 | ISR on demand: revalidatePath from the dashboard reaches every instance | pass | new revision everywhere after 150 ms; 1 distinct render time(s) across instances (201 ms) |
| F18 | use cache + updateTag: restock refreshes /products everywhere, one shared entry | pass | 1 shared entry before and after (321 ms) |
| F19 | PPR: /products static shell first, cart count streamed from the cookie | pass | grid at 9 ms of 13 ms; add to cart: "Cart: …" → "Cart: 1" (195 ms) |
| F20 | route handler /api/items: JSON from Postgres on every instance | pass | 12 items, instances 0,1, encoding=none (60 ms) |
| F21 | SSE /api/stream: events arrive as sent (browser encoding) | pass | zstd: 10 events, first 6 ms, last 1814 ms; identity: first 10 ms (3633 ms) |
| F22 | next/image: a new image is optimized, then served from the image cache | pass | cold 106 ms (image/webp, 24212 B), warm 6 ms HIT (113 ms) |
| F23 | no console errors, failed requests or 4xx/5xx in the browser | pass | clean (0 ms) |

## Lighthouse (median of 5 runs)

| Page | Form factor | Score | FCP ms | LCP ms | TBT ms | CLS | SI ms | TTFB ms | KB |
|---|---|---|---|---|---|---|---|---|---|
| / | mobile | 99 | 769 | 2015 | 7 | 0.000 | 769 | 21 | 262 |
| / | desktop | 100 | 204 | 459 | 0 | 0.000 | 204 | 11 | 284 |
| /blog/hello-box | mobile | 100 | 762 | 1866 | 12 | 0.000 | 762 | 13 | 208 |
| /blog/hello-box | desktop | 100 | 207 | 421 | 0 | 0.000 | 207 | 16 | 208 |
| /products | mobile | 100 | 765 | 1871 | 18 | 0.000 | 765 | 10 | 209 |
| /products | desktop | 100 | 207 | 420 | 0 | 0.000 | 207 | 10 | 209 |
| /dashboard | mobile | 79 | 764 | 1803 | 7 | 0.463 | 764 | 5 | 211 |
| /dashboard | desktop | 100 | 206 | 404 | 0 | 0.028 | 206 | 5 | 211 |

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 999.8 | 100 | 0.4 | 5 | 43.6 | 95.8 | - |
| home (static) | vzNAT | 400 | 400 | 100 | 1.9 | 85 | 238.1 | 439.1 | - |
| home (static) | forwarder | 400 | 400 | 100 | 2.6 | 805.8 | 1057.6 | 1302.3 | - |
| blog (ISR) | vzNAT | 400 | 399.8 | 100 | 40.6 | 1883.2 | 2051.7 | 2455.3 | - |
| products (PPR) | vzNAT | 200 | 199.4 | 100 | 10 | 189.5 | 310.8 | 430.6 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 7.5 | 12.5 | 18.3 | 37.2 | - |
| api/items (JSON) | vzNAT | 400 | 399.7 | 100 | 1.5 | 4.6 | 22.2 | 72.5 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 1.1 | 2.2 | 5.1 | 33.8 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 33.1 | 51.9 | 90 | 94.6 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 2.9 | 5.7 | 29.5 | 140.1 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | node | 179 | 112 | 320 | 290 | 179 | 328 |
| 1 | node | 49 | 114 | 186 | 185 | 230 | 186 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1957 | 1939 | 340 | 13 | 284 | 514 | 49 |
| after load | 1891 | 2005 | 629 | 13 | 196 | 361 | 49 |

