# Showcase bench: before and after the Next.js fixes

The 2026-10-06 baselines (`a-bun-x2`, `b-node-x2`, `c-bun-x1`) against the same bench on a
fresh box built from the fixed tree (`after-*`). Same machine, same box shape (Lima, 2 vCPU,
4 GiB), same load profile. What changed between them:

- prerendered pages and route handlers are served from the build (no render on first request);
- builds get the app's env read-only (database, Valkey), so prerenders read real data;
- `/_next/image` for the project's bucket files is answered by the box's image transforms;
- `VERCEL_PROJECT_PRODUCTION_URL` gives Next.js the app's origin for og:image;
- client files are precompressed (zstd, gzip) and count toward a separate per-IP bucket;
- Next.js starts as one process (`exec bun --bun ./node_modules/next/dist/bin/next start`);
- `poweredByHeader: false`; the showcase seeds through `release` instead of on start;
- shared cache, revalidation by page tags, and the Node RESP client had landed before these
  runs too (they fix F17, F18 and the Node cache).

F09 now asks that the first request after a deploy is served from the build's prerender
(`x-nextjs-cache` HIT or STALE) and that the next two requests agree; before, it compared two
requests only.

## Summary

| | Bun ×2 before → after | Node ×2 before → after | Bun ×1 before → after |
|---|---|---|---|
| Functional checks | 18 → 23 of 23 | 17 → 23 of 23 | 17 → 23 of 23 |
| static chunk p95 / p99 ms | 234.5 / 349.2 → 0.7 / 1.8 | 11.3 / 35.8 → 5.0 / 43.6 | 89.6 / 276.5 → 1.3 / 5.7 |
| home p95 ms | 12.6 → 9.4 | 4.5 → 85.0 (repeat: 7.3) | 7.5 → 2.6 |
| blog (ISR) p95 ms | 9.4 → 2.9 | 2.1 → 1883 (repeat: 7.5) | 2.8 → 149 |
| server action p95 / p99 ms | 14.7 / 48.4 → 4.4 / 5.5 | 56.4 / 605 → 5.7 / 29.5 (repeat: 404 / 722) | 12.6 / 78.1 → 5.5 / 8.8 |
| memory per instance, peak MB (cgroup) | 357 / 356 → 223 / 240 | 336 / 183 → 320 / 186 | 424 → 394 |

Notes on the numbers:

- **Static chunks** were the biggest change: the box answers them from disk with a
  precompressed copy, so the edge no longer compresses 1,000 responses a second.
- **Node stalls.** Node had multi-second stalls in one step of each run (blog in the
  first, server actions in the repeat: same deploy, load only); Bun had none in the 2-instance
  runs. The Node log shows a `TimeoutNegativeWarning` from Next.js at those moments. One Bun
  instance (×1) shows one too (blog p95 149 ms): an ISR page past its 60 s revalidate is
  re-rendered in the background, which on a single instance holds up its event loop briefly.
- **Memory.** Bun instances settle at ~270 MB RSS after load (cgroup 214–232 MB) instead of
  306–353 MB RSS (cgroup 316–330 MB): one process instead of two, and no runtime
  re-render of every prerendered page. `memory.current` includes page cache, which a burst of
  image writes can push up for a while (one earlier run: 456 MB with 238 MB of it file cache).
  Bun's own knobs made no difference (below), so the box sets none.
- **Lighthouse** is unchanged except `/dashboard` on mobile: CLS 0.43 (score 80) in both
  after-runs, as in the Node baseline (79); the Bun baseline got 0.06. The dashboard's static
  shell now arrives at once, so the stats skeleton (80 px) paints before the four stat cards
  (stacked on a phone, ~280 px) replace it and push the page down: the showcase's skeleton,
  not the platform. A single later run measured 0.06 when the stats arrived with the shell.

## Bun memory knobs (2 instances, load and memory only)

`mem-bun-default` ran with `BUN_JSC_forceRAMSize` = 512 MiB (the cap), `mem-bun-noknob` with
4 GiB (the machine, as without it), `mem-bun-smol` with `bun --smol`.

| Instance (idle / peak / after, MB) | forceRAMSize 512 MiB | 4 GiB | --smol |
|---|---|---|---|
| 0 | 73 / 229 / 216 | 84 / 237 / 228 | 169 / 316 / 307 |
| 1 | 78 / 230 / 221 | 70 / 225 / 215 | 93 / 253 / 243 |

RSS after load was 264–269 MB in all three; latencies were the same within noise.

## All runs side by side

From `node compare.ts` (repeat and memory runs left out where they have no checks or
Lighthouse).

### Functional checks

| Check | a-bun-x2 | after-a-bun-x2 | b-node-x2 | after-b-node-x2 | c-bun-x1 | after-c-bun-x1 |
| --- | --- | --- | --- | --- | --- | --- |
| F05 next/image remote: the bucket's featured image loads | FAIL | pass | FAIL | pass | FAIL | pass |
| F06 Open Graph: og:image is an absolute URL on this site | FAIL | pass | FAIL | pass | FAIL | pass |
| F08 client navigation (RSC) from / to /products | pass | pass | pass | pass | FAIL | pass |
| F09 blog: prerendered at build, served from cache (ISR) | pass | pass | FAIL | pass | pass | pass |
| F17 ISR on demand: revalidatePath from the dashboard reaches every instance | FAIL | pass | FAIL | pass | FAIL | pass |
| F18 use cache + updateTag: restock refreshes /products everywhere, one shared entry | FAIL | pass | FAIL | pass | FAIL | pass |
| F23 no console errors, failed requests or 4xx/5xx in the browser | FAIL | pass | FAIL | pass | FAIL | pass |
| the other 16 | pass | pass | pass | pass | pass | pass |

### Load: p50 / p95 / p99 ms (achieved req/s)

| Target | Via | Rate | a-bun-x2 | after-a-bun-x2 | b-node-x2 | after-b-node-x2 | after-b-node-x2-repeat | c-bun-x1 | after-c-bun-x1 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| static chunk | vzNAT | 1000 | 0.5 / 234.5 / 349.2 (999.9) | 0.4 / 0.7 / 1.8 (999.9) | 0.5 / 11.3 / 35.8 (999.9) | 0.4 / 5 / 43.6 (999.8) | 0.4 / 1.8 / 7.4 (999.9) | 0.9 / 89.6 / 276.5 (999.9) | 0.4 / 1.3 / 5.7 (1000) |
| home (static) | vzNAT | 400 | 1.4 / 12.6 / 158.1 (400) | 1.5 / 9.4 / 28.5 (400) | 1.2 / 4.5 / 17.6 (399.5) | 1.9 / 85 / 238.1 (400) | 1.6 / 7.3 / 41.8 (400) | 1.2 / 7.5 / 32.2 (399.9) | 1.3 / 2.6 / 5 (400) |
| home (static) | forwarder | 400 | 1.2 / 2.9 / 6.7 (400) | 1.7 / 10 / 32.1 (400) | 1.4 / 7.6 / 48.9 (400) | 2.6 / 805.8 / 1057.6 (400) | 1.7 / 5.4 / 16.7 (399.9) | 1.4 / 11.2 / 66.2 (400) | 1.5 / 14 / 51.8 (399.9) |
| blog (ISR) | vzNAT | 400 | 1.2 / 9.4 / 27.4 (399.9) | 1.3 / 2.9 / 7.1 (400) | 1.1 / 2.1 / 4.3 (400) | 40.6 / 1883.2 / 2051.7 (399.8) | 2 / 7.5 / 20.8 (400) | 1 / 2.8 / 10.1 (400) | 2.7 / 149 / 264.3 (400) |
| products (PPR) | vzNAT | 200 | 3.7 / 10.1 / 27.1 (200) | 4.3 / 9 / 29.4 (200) | 4.5 / 15.6 / 47.4 (199.5) | 10 / 189.5 / 310.8 (199.4) | 7.9 / 83.8 / 208.8 (200) | 4.6 / 18.5 / 77.5 (199.9) | 3.7 / 7.9 / 18.8 (200) |
| dashboard (dynamic) | vzNAT | 50 | 6.1 / 9.3 / 13.1 (50) | 5.8 / 15.1 / 46.3 (50) | 6.7 / 9.4 / 12.1 (50) | 7.5 / 12.5 / 18.3 (50) | 7.3 / 16.5 / 35.2 (50) | 8.8 / 154.6 / 294.4 (50) | 5.3 / 9.1 / 19.3 (50) |
| api/items (JSON) | vzNAT | 400 | 1 / 2.9 / 12.2 (400) | 1.3 / 15.1 / 96.9 (400) | 1.2 / 4.4 / 13.9 (399.9) | 1.5 / 4.6 / 22.2 (399.7) | 1.7 / 8.7 / 34.7 (400) | 2.3 / 699.3 / 799.8 (400) | 1.1 / 2.3 / 4.2 (400) |
| next/image warm | vzNAT | 300 | 0.9 / 2.6 / 5.7 (300) | 1.1 / 2.9 / 18.7 (300) | 1.1 / 28.6 / 65.2 (300) | 1.1 / 2.2 / 5.1 (300) | 1.2 / 4.5 / 16 (300) | 0.6 / 2.8 / 12.3 (300) | 0.9 / 1.7 / 3.7 (300) |
| next/image cold | vzNAT | 10 | 30.1 / 42.9 / 113.1 (10) | 29.6 / 38.4 / 46.7 (10) | 30.6 / 42 / 51.7 (10) | 33.1 / 51.9 / 90 (10) | 39.4 / 123 / 240.4 (10) | 36.4 / 181.1 / 319.2 (10) | 28.9 / 35.2 / 40.4 (10) |
| server action POST | vzNAT | 100 | 2.6 / 14.7 / 48.4 (100) | 2.5 / 4.4 / 5.5 (100) | 3.3 / 56.4 / 605 (100) | 2.9 / 5.7 / 29.5 (100) | 4.4 / 403.9 / 721.9 (100) | 2.3 / 12.6 / 78.1 (100) | 2.6 / 5.5 / 8.8 (100) |

### Lighthouse: score, LCP ms, TBT ms (after-c-bun-x1 ran without Lighthouse)

| Page | Form factor | a-bun-x2 | after-a-bun-x2 | b-node-x2 | after-b-node-x2 |
| --- | --- | --- | --- | --- | --- |
| / | mobile | 99, 2066, 15 | 99, 2129, 7 | 99, 2057, 7 | 99, 2015, 7 |
| / | desktop | 100, 458, 0 | 100, 470, 0 | 100, 457, 0 | 100, 459, 0 |
| /blog/hello-box | mobile | 100, 1867, 14 | 100, 1862, 10 | 100, 1861, 9 | 100, 1866, 12 |
| /blog/hello-box | desktop | 100, 417, 0 | 100, 420, 0 | 100, 417, 0 | 100, 421, 0 |
| /products | mobile | 100, 1866, 12 | 100, 1861, 9 | 100, 1862, 10 | 100, 1871, 18 |
| /products | desktop | 100, 419, 0 | 100, 417, 0 | 100, 417, 0 | 100, 420, 0 |
| /dashboard | mobile | 99, 1863, 11 | 80, 1803, 0 | 79, 1802, 5 | 79, 1803, 7 |
| /dashboard | desktop | 100, 419, 0 | 100, 402, 0 | 100, 417, 0 | 100, 404, 0 |

### Memory per instance, MB (idle / peak under load / after, cgroup)

| Instance | a-bun-x2 | after-a-bun-x2 | b-node-x2 | after-b-node-x2 | after-b-node-x2-repeat | c-bun-x1 | after-c-bun-x1 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0 | 302 / 357 / 330 | 79 / 223 / 214 | 216 / 336 / 336 | 179 / 320 / 290 | 298 / 339 / 339 | 205 / 424 / 392 | 153 / 394 / 378 |
| 1 | 262 / 356 / 316 | 76 / 240 / 232 | 60 / 183 / 165 | 49 / 186 / 185 | 159 / 193 / 192 | - | - |
