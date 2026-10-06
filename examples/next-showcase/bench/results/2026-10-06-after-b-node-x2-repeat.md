# Showcase bench: after-b-node-x2-repeat

2026-10-06 03:43 UTC. Target https://next-showcase.tiffin.localhost:54686, load via the VM's address 192.168.64.4, VM tiffin-bench-1791254123.

- runtime: node
- instances: 2
- deploy: dep_01M47M0RKA5XE033S2496KD95F
- tiffin: {"commit":"9f6d584+","date":"unknown","name":"tiffin","version":"0.0.1-bench"}
- box: 2 vCPU, 3896 MB, 7.0.0-34-generic

> Load and memory again on the same Node deploy (the first run had a 2 s stall in the blog step).

## Load (oha, fixed rate, latency-corrected)

| Target | Via | Rate/s | Got req/s | 2xx % | p50 ms | p95 ms | p99 ms | max ms | Errors |
|---|---|---|---|---|---|---|---|---|---|
| static chunk | vzNAT | 1000 | 999.9 | 100 | 0.4 | 1.8 | 7.4 | 25.4 | - |
| home (static) | vzNAT | 400 | 400 | 100 | 1.6 | 7.3 | 41.8 | 145.4 | - |
| home (static) | forwarder | 400 | 399.9 | 100 | 1.7 | 5.4 | 16.7 | 52.8 | - |
| blog (ISR) | vzNAT | 400 | 400 | 100 | 2 | 7.5 | 20.8 | 50.6 | - |
| products (PPR) | vzNAT | 200 | 200 | 100 | 7.9 | 83.8 | 208.8 | 339.8 | - |
| dashboard (dynamic) | vzNAT | 50 | 50 | 100 | 7.3 | 16.5 | 35.2 | 98.9 | - |
| api/items (JSON) | vzNAT | 400 | 400 | 100 | 1.7 | 8.7 | 34.7 | 145.8 | - |
| next/image warm | vzNAT | 300 | 300 | 100 | 1.2 | 4.5 | 16 | 73.6 | - |
| next/image cold | vzNAT | 10 | 10 | 100 | 39.4 | 123 | 240.4 | 248 | - |
| server action POST | vzNAT | 100 | 100 | 100 | 4.4 | 403.9 | 721.9 | 1094.4 | - |

## Memory per instance (MB)

| Instance | Runtime | Idle cgroup | Idle RSS | Peak during load | After load cgroup | After load RSS | memory.peak |
|---|---|---|---|---|---|---|---|
| 0 | node | 298 | 187 | 339 | 339 | 224 | 339 |
| 1 | node | 159 | 203 | 193 | 192 | 234 | 193 |

| Box | used | available | postgres | valkey-server | tiffin | buildkitd | containerd |
|---|---|---|---|---|---|---|---|
| idle | 1830 | 2066 | 342 | 13 | 167 | 361 | 50 |
| after load | 1986 | 1910 | 786 | 13 | 220 | 361 | 47 |

