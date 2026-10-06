# Showcase bench: mem-bun-default

2026-10-06 02:53 UTC. Target https://next-showcase.tiffin.localhost:54686, load via the VM's address 192.168.64.4, VM tiffin-bench-1791254123.

- runtime: bun
- instances: 2
- deploy: dep_01M47HZS0H06KM2G17V6B6X4SD
- tiffin: {"commit":"9f6d584+","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Memory knob test: BUN_JSC_forceRAMSize = 512 MiB (the box default)

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 999.9 | 100 | 0.4 | 1.4 | 13.4 | 65.5 | - |
| home (static) | vzNAT | 400 | 400 | 100 | 1.2 | 2.2 | 4.1 | 24 | - |
| home (static) | forwarder | 400 | 399.9 | 100 | 1.4 | 2.8 | 6.5 | 18.2 | - |
| blog (ISR) | vzNAT | 400 | 400 | 100 | 1.4 | 2.8 | 5.6 | 22.4 | - |
| products (PPR) | vzNAT | 200 | 200 | 100 | 3.9 | 6.8 | 15.1 | 69.6 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 5.4 | 7.8 | 10.8 | 21.5 | - |
| api/items (JSON) | vzNAT | 400 | 400 | 100 | 1.2 | 2.4 | 4.4 | 15.5 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 0.9 | 1.7 | 2.9 | 20 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 31.6 | 38.3 | 41.3 | 45.6 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 2.7 | 4.4 | 6.3 | 12 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | bun | 73 | 118 | 229 | 216 | 268 | 230 |
| 1 | bun | 78 | 120 | 230 | 221 | 268 | 232 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1530 | 2366 | 357 | 12 | 168 | 291 | 50 |
| after load | 1831 | 2065 | 404 | 13 | 204 | 218 | 49 |

