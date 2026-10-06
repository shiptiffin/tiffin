# Showcase bench: mem-bun-smol

2026-10-06 03:09 UTC. Target https://next-showcase.tiffin.localhost:54686, load via the VM's address 192.168.64.4, VM tiffin-bench-1791254123.

- runtime: bun
- instances: 2
- deploy: dep_01M47JZ4DKQ745VR8Q4VXS5GE1
- tiffin: {"commit":"9f6d584+","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Memory knob test: bun --smol (and the box default BUN_JSC_forceRAMSize = 512 MiB)

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 999.9 | 100 | 0.4 | 0.6 | 1.6 | 12.1 | - |
| home (static) | vzNAT | 400 | 400 | 100 | 1.2 | 2.3 | 4.1 | 29.3 | - |
| home (static) | forwarder | 400 | 400 | 100 | 1.6 | 3.4 | 8.3 | 41.8 | - |
| blog (ISR) | vzNAT | 400 | 400 | 100 | 1.2 | 2.4 | 4.3 | 15.6 | - |
| products (PPR) | vzNAT | 200 | 200 | 100 | 4.5 | 9.6 | 23.4 | 58.6 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 5.7 | 7.7 | 10.5 | 20.9 | - |
| api/items (JSON) | vzNAT | 400 | 400 | 100 | 1 | 2.2 | 4.6 | 16.2 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 0.9 | 1.9 | 4 | 23.8 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 28.5 | 35.6 | 39.5 | 43.6 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 2.8 | 4.5 | 6.9 | 11.3 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | bun | 169 | 123 | 316 | 307 | 269 | 317 |
| 1 | bun | 93 | 123 | 253 | 243 | 267 | 254 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1872 | 2024 | 359 | 13 | 241 | 485 | 50 |
| after load | 1934 | 1962 | 404 | 13 | 204 | 306 | 50 |

