// The showcase bench: functional checks, Lighthouse, fixed-rate load and
// per-instance memory against one deployment, written to
// bench/results/<date>-<label>.md (and .json).
//
//   node run.ts --base https://next-showcase.tiffin.localhost:8443 --label bun-x2 \
//     [--vm <lima instance>] [--direct-ip <vm address>] [--instances 2] \
//     [--only checks,lighthouse,load,memory] [--runs 5] [--seconds 20]
//
// `eval "$(bench/box.sh env)"` prints BASE, VM and DIRECT_IP for a bench box.
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { functional } from "./lib/functional.ts";
import { lighthouse } from "./lib/lighthouse.ts";
import { oha, profile } from "./lib/load.ts";
import { get, type Target } from "./lib/net.ts";
import { markdown, type Run } from "./lib/report.ts";
import { actionIds, boxMemory, memory, sampler, sh } from "./lib/vm.ts";

const { values: a } = parseArgs({
  options: {
    base: { type: "string", default: process.env.BASE },
    label: { type: "string", default: "run" },
    vm: { type: "string", default: process.env.VM },
    "direct-ip": { type: "string", default: process.env.DIRECT_IP },
    instances: { type: "string", default: "2" },
    only: { type: "string", default: "checks,lighthouse,load,memory" },
    runs: { type: "string", default: "5" },
    seconds: { type: "string", default: "20" },
    note: { type: "string", multiple: true, default: [] },
  },
});
if (!a.base) throw new Error("--base (or BASE) is required");

const t: Target = { base: a.base.replace(/\/$/, ""), directIp: a["direct-ip"] || undefined, cookie: "showcase_session=demo" };
const only = new Set(a.only!.split(","));
const date = new Date().toISOString().slice(0, 10);
const run: Run = { label: a.label!, date: new Date().toISOString().replace("T", " ").slice(0, 16) + " UTC", base: t.base, directIp: t.directIp, vm: a.vm, notes: [...a.note!] };
const instances = Number(a.instances);
const vm = a.vm;

const health = JSON.parse((await get(t, "/api/health")).body.toString()) as { runtime: string };
run.deploy = { runtime: health.runtime, instances: String(instances) };
if (vm) {
  run.deploy.deploy = sh(vm, `nerdctl -n tiffin ps --filter label=tiffin.project=next-showcase --format '{{.Labels}}' | grep -o 'tiffin.deploy=[^,]*' | sort -u | cut -d= -f2 | tr '\\n' ' '`);
  run.deploy.tiffin = sh(vm, "/usr/local/bin/tiffin version 2>/dev/null | tr -d '\\n ' || true");
  run.deploy.box = sh(vm, `echo "$(nproc) vCPU, $(free -m | awk '/^Mem:/{print $2}') MB, $(uname -r)"`);
}
const actions = vm ? actionIds(vm) : {};
console.log(`bench ${run.label}: ${t.base} (${health.runtime}, ${instances} instance(s))`);

if (vm && only.has("memory")) {
  run.memory = { idle: memory(vm), boxIdle: boxMemory(vm) };
}
if (only.has("checks")) {
  console.log("functional checks");
  run.checks = await functional(t, { instances, actions });
}
if (only.has("lighthouse")) {
  console.log("lighthouse");
  run.lighthouse = lighthouse(t, ["/", "/blog/hello-box", "/products", "/dashboard"], Number(a.runs));
}
if (only.has("load")) {
  console.log("load");
  const home = (await get(t, "/")).body.toString();
  const chunk = home.match(/src="(\/_next\/static\/chunks\/[^"?]+\.js(?:\?[^"]*)?)"/)?.[1] ?? "/_next/static/missing.js";
  // Warm what the warm targets read, then measure.
  await get(t, "/_next/image?url=%2Fhero.jpg&w=828&q=75", { headers: { accept: "image/avif,image/webp,*/*" } });
  const s = vm && only.has("memory") ? sampler(vm) : undefined;
  run.load = [];
  for (const spec of profile(t, { chunk, actionId: actions.likeProduct })) {
    oha(t, spec, 3, true); // warm-up, not recorded
    run.load.push(oha(t, spec, Number(a.seconds)));
  }
  if (s && run.memory) run.memory.peakDuringLoad = await s.stop();
}
if (vm && only.has("memory")) {
  await new Promise((r) => setTimeout(r, 10_000));
  run.memory = { ...run.memory, after: memory(vm), boxAfter: boxMemory(vm) };
}

const dir = join(import.meta.dirname, "results");
mkdirSync(dir, { recursive: true });
const file = join(dir, `${date}-${run.label}`);
writeFileSync(file + ".md", markdown(run) + "\n");
writeFileSync(file + ".json", JSON.stringify(run, null, 2) + "\n");
console.log(`wrote ${file}.md`);
