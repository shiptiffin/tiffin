# Showcase bench

Measures `examples/next-showcase` on a Tiffin box from this computer: functional checks
(raw HTTP and Chromium), Lighthouse, fixed-rate load with oha, and the memory of each
instance. Each run writes `results/<date>-<label>.md` and `.json`. Rerun it after a
platform change and compare the files.

Needs Node.js 24, Lima, `brew install oha`, and in this folder `npm install && npm run setup`
(Playwright's Chromium, also used by Lighthouse).

```bash
bench/box.sh up                      # a fresh box from this checkout (2 vCPU / 4 GiB), ~3 min
bench/box.sh deploy bun 2            # as Tiffin deploys it: bun --bun next start, 2 instances
eval "$(bench/box.sh env)"           # BASE, VM, DIRECT_IP
cd bench && node run.ts --label bun-x2 --instances 2

bench/box.sh deploy node 2           # the same build started with Node.js
bench/box.sh deploy bun 1            # one instance
bench/box.sh down                    # always, when done

node compare.ts results/<a>.json results/<b>.json   # side by side
```

`run.ts --only checks,lighthouse,load,memory` picks parts; `--runs` (Lighthouse, default 5)
and `--seconds` (each load step, default 20) trade time for precision. Run heavy steps
through `research/heavy.sh` when other jobs share the machine.

What it does, and what to keep in mind when reading the numbers:

- **Box:** `box.sh up` builds tiffin from this checkout and creates the VM from
  `internal/provider/lima/box.yaml` as `tiffin up` does, plus a vzNAT address, with its CLI
  config in `bench/.work` (never `~/.tiffin`). It raises the per-IP app rate limit so the
  load generator is not answered with 429s.
- **Checks** go through Lima's port forwarder (`127.0.0.1:<port>`), like a browser on this
  computer. **Load** goes to the VM's own address (port 8443) unless a step says
  `forwarder`; one step runs both ways to show what the forwarder costs.
- **Load profile** (`lib/load.ts`): fixed rates with `--latency-correction`, 64
  connections, HTTP/1.1, 3 s warm-up per step. Keep it unchanged between runs you compare.
- **Memory** is read inside the VM: each instance's cgroup (`memory.current`,
  `memory.peak`), the summed RSS of its processes, sampled every second during load.
- Lighthouse runs on this computer's CPU with its default throttling: compare runs made on
  the same machine.
