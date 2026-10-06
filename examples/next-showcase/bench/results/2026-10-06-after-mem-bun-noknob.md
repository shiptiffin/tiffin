# Showcase bench: mem-bun-noknob

2026-10-06 03:04 UTC. Target https://next-showcase.tiffin.localhost:54686, load via the VM's address 192.168.64.4, VM tiffin-bench-1791254123.

- runtime: bun
- instances: 2
- deploy: dep_01M47JC34WE859J4Q2D8GVNSWQ
- tiffin: {"commit":"9f6d584+","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Memory knob test: BUN_JSC_forceRAMSize = 4 GiB (the machine; as without the knob)

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 999.9 | 100 | 0.3 | 0.7 | 2 | 23.3 | - |
| home (static) | vzNAT | 400 | 399.9 | 100 | 1.4 | 3.8 | 36.2 | 81 | - |
| home (static) | forwarder | 400 | 398.4 | 100 | 2.5 | 799.6 | 1115.5 | 1281.2 | - |
| blog (ISR) | vzNAT | 400 | 400 | 100 | 1.7 | 5.7 | 33.3 | 87 | - |
| products (PPR) | vzNAT | 200 | 200 | 100 | 4.5 | 8.1 | 17.3 | 38.7 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 5.5 | 8.3 | 11.9 | 25 | - |
| api/items (JSON) | vzNAT | 400 | 400 | 100 | 1.1 | 2.4 | 4.5 | 23.5 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 1 | 1.9 | 3.6 | 14.3 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 30.4 | 37.7 | 44.2 | 48.7 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 2.9 | 4.8 | 7.6 | 14.9 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | bun | 84 | 118 | 237 | 228 | 265 | 239 |
| 1 | bun | 70 | 117 | 225 | 215 | 265 | 226 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1535 | 2361 | 359 | 13 | 172 | 260 | 50 |
| after load | 1870 | 2026 | 449 | 13 | 205 | 260 | 50 |

