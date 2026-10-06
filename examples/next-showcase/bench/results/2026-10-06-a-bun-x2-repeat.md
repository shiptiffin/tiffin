# Showcase bench: a-bun-x2-repeat

2026-10-06 01:42 UTC. Target https://next-showcase.tiffin.localhost:49581, load via the VM's address 192.168.64.3, VM tiffin-bench-1791247088.

- runtime: bun
- instances: 2
- deploy: dep_01M47E0K60HN9N0Q2WE0HBX7FV
- tiffin: {"commit":"none","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Repeat of (a) after redeploying the same source, to show run-to-run noise (other VMs shared the Mac).

> Per-IP app rate limit raised to 1,000,000/10 s for the load generator.

## Functional checks: 16/23 pass

|  | Check | Result | Details |
|---|---|---|---|
| F01 | home: static, proxy header, compressed | pass | cache-control=s-maxage=31536000 x-nextjs-cache=HIT x-nextjs-prerender=1 encoding=zstd 18349→4635 B (20 ms) |
| F02 | next/font: woff2 preloaded and served immutable | pass | Geist_Variable-s.p.03oqkqkevsckn.woff2?dpl=dep_01M47AHN1XD8BZ5KKFEJ995DC0 font/woff2 public, max-age=31536000, immutable (3 ms) |
| F03 | client chunk: immutable, compressed by the edge | pass | cache-control=public, max-age=31536000, immutable content-encoding=zstd (3 ms) |
| F04 | next/image local: every srcset candidate loads | pass | 8 widths, image/webp, browser picked 1080w (62 ms) |
| F05 | next/image remote: the bucket's featured image loads | **FAIL** | optimizer → 400 ""url" parameter is not allowed"; the file itself (https://files.tiffin.localhost:49581/next-showcase/media/featured.jpg) → 200 image/jpeg |
| F06 | Open Graph: og:image is an absolute URL on this site | **FAIL** | og:image is http://localhost:20001/opengraph-image?3f305df0986b521c (no metadataBase: Next.js falls back to localhost) |
| F07 | Open Graph image decodes (1200×630 PNG) | pass | 71533 B, cache-control=public, max-age=0, must-revalidate (18 ms) |
| F08 | client navigation (RSC) from / to /products | **FAIL** | full page load instead of a client navigation |
| F09 | blog: prerendered at build, served from cache (ISR) | pass | cache-control=s-maxage=60, stale-while-revalidate=3540 x-nextjs-cache=STALE x-nextjs-prerender=1 (9 ms) |
| F10 | proxy: /dashboard without a session redirects to /login on this site | pass | 307 → /login (16 ms) |
| F11 | login: form Server Action sets the session, redirects to /dashboard | pass | cookie secure=false httpOnly=true (1190 ms) |
| F12 | streaming: dashboard shell arrives before the slow section (edge, browser encoding) | pass | zstd: shell 9 ms, slow section 1017 ms, 7 chunks; uncompressed: shell 7 ms (2040 ms) |
| F13 | streaming in Chromium: shell painted before the slow section | pass | shell 52 ms, slow section 1350 ms (1392 ms) |
| F14 | Server Action: useActionState + useOptimistic note, then saved | pass | optimistic 27 ms, saved 822 ms (400 ms of it is the action's own wait), kept after reload (1655 ms) |
| F15 | Server Action: useOptimistic like, round trip | pass | optimistic 18 ms, server round trip 29 ms (42 ms) |
| F16 | Server Action over fetch (Next-Action header, as in the load test) | pass | 200 45 B in 9 ms (9 ms) |
| F17 | ISR on demand: revalidatePath from the dashboard reaches every instance | **FAIL** | still serving rev 6 after 10 s |
| F18 | use cache + updateTag: restock refreshes /products everywhere, one shared entry | **FAIL** | old cache entry still served: before 2026-10-06T00:44:28.929Z, after 2026-10-06T00:44:28.929Z |
| F19 | PPR: /products static shell first, cart count streamed from the cookie | **FAIL** | page.waitForFunction: Timeout 10000ms exceeded. |
| F20 | route handler /api/items: JSON from Postgres on every instance | pass | 12 items, instances 0,1, encoding=none (61 ms) |
| F21 | SSE /api/stream: events arrive as sent (browser encoding) | pass | zstd: 10 events, first 5 ms, last 1823 ms; identity: first 7 ms (3642 ms) |
| F22 | next/image: a new image is optimized, then served from the image cache | pass | cold 95 ms (image/webp, 24212 B), warm 5 ms HIT (100 ms) |
| F23 | no console errors, failed requests or 4xx/5xx in the browser | **FAIL** | HTTP 400 /_next/image?url=https%3A%2F%2Ffiles.tiffin.localhost%3A49581%2Fnext-showcase%2Fmedia%2Ffeatured.jpg&w=640&q=75 \| console: Failed to load resource: the server responded with a status of 400 () (https://next-showcase.tiffin.localhost:49581/) \| pageerror: Minified React error #418; visit https://react.dev/errors/418?args[]=text&args[]= for the full message or use the non-minified dev enviro |

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 999.9 | 100 | 0.5 | 0.9 | 2.4 | 13.1 | - |
| home (static) | vzNAT | 400 | 400 | 100 | 1.2 | 2.5 | 5.2 | 19.3 | - |
| home (static) | forwarder | 400 | 400 | 100 | 1.4 | 3.3 | 8.4 | 32.3 | - |
| blog (ISR) | vzNAT | 400 | 400 | 100 | 1.4 | 19.9 | 56.6 | 164.8 | - |
| products (PPR) | vzNAT | 200 | 200 | 100 | 4.5 | 10 | 20.6 | 48 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 5.5 | 8.3 | 13.6 | 27.9 | - |
| api/items (JSON) | vzNAT | 400 | 400 | 100 | 1.1 | 2.2 | 4.2 | 16.6 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 0.9 | 1.9 | 3.5 | 21.6 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 30.8 | 38.8 | 47.8 | 76 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 2.4 | 4.2 | 6.7 | 24.7 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | bun | 211 | 142 | 396 | 374 | 290 | 413 |
| 1 | bun | 77 | 137 | 249 | 229 | 284 | 259 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1788 | 2108 | 425 | 12 | 177 | 469 | 55 |
| after load | 1939 | 1957 | 379 | 12 | 217 | 315 | 50 |

