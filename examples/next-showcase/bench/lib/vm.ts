// Reads from inside the Lima VM (limactl shell): the build's Server Action
// IDs and the memory of each app instance (its cgroup and process RSS).
import { execFileSync, spawn, type ChildProcess } from "node:child_process";

export function sh(vm: string, script: string): string {
  return execFileSync("limactl", ["shell", "--workdir", "/", vm, "--", "sudo", "bash", "-c", script], {
    encoding: "utf8",
    maxBuffer: 64 << 20,
  }).trim();
}

const containers = `sudo nerdctl -n tiffin ps -q --filter label=tiffin.project=next-showcase --filter label=tiffin.app=web`;

/** exportedName → action ID, from .next/server/server-reference-manifest.json in a live instance. */
export function actionIds(vm: string): Record<string, string> {
  const id = sh(vm, `${containers} | head -1`);
  if (!id) return {};
  const m = JSON.parse(sh(vm, `nerdctl -n tiffin exec ${id} cat /app/.next/server/server-reference-manifest.json`)) as {
    node: Record<string, { exportedName?: string }>;
  };
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(m.node)) if (v.exportedName) out[v.exportedName] = k;
  return out;
}

export type Mem = { name: string; instance: string; runtime: string; currentMB: number; peakMB: number; anonMB: number; rssMB: number };

// One line per instance: name, TIFFIN_INSTANCE, runtime, memory.current,
// memory.peak, anon (bytes) and the summed VmRSS of its processes (kB).
const memScript = `
for c in $(${containers}); do
  pid=$(nerdctl -n tiffin inspect -f '{{.State.Pid}}' $c)
  name=$(nerdctl -n tiffin inspect -f '{{.Name}}' $c)
  inst=$(tr '\\0' '\\n' < /proc/$pid/environ | sed -n 's/^TIFFIN_INSTANCE=//p')
  cg=/sys/fs/cgroup$(awk -F: '$1=="0"{print $3}' /proc/$pid/cgroup)
  rss=0; rt=bun
  for p in $(cat $cg/cgroup.procs); do
    r=$(awk '/^VmRSS/{print $2}' /proc/$p/status 2>/dev/null); rss=$((rss + \${r:-0}))
    case "$(readlink /proc/$p/exe 2>/dev/null)" in */node) rt=node;; esac
  done
  echo "$name $inst $rt $(cat $cg/memory.current) $(cat $cg/memory.peak) $(awk '$1=="anon"{print $2}' $cg/memory.stat) $rss"
done`;

export function memory(vm: string): Mem[] {
  return sh(vm, memScript)
    .split("\n")
    .filter(Boolean)
    .map((l) => {
      const [name, instance, runtime, cur, peak, anon, rss] = l.split(" ");
      const mb = (b: string) => Math.round(Number(b) / 1048576);
      return { name: name!, instance: instance!, runtime: runtime!, currentMB: mb(cur!), peakMB: mb(peak!), anonMB: mb(anon!), rssMB: Math.round(Number(rss) / 1024) };
    })
    .sort((a, b) => a.instance.localeCompare(b.instance));
}

/** The box as a whole: used memory and the main services' RSS (MB). */
export function boxMemory(vm: string): Record<string, number> {
  const out = sh(vm, `free -m | awk '/^Mem:/{print "used", $3; print "available", $7}'
for p in postgres valkey-server tiffin buildkitd containerd; do
  echo "$p $(ps -C $p -o rss= | awk '{s+=$1} END {print int(s/1024)}')"
done`);
  return Object.fromEntries(out.split("\n").map((l) => l.split(" ")).map(([k, v]) => [k!, Number(v)]));
}

/** Samples each instance's memory.current once a second until stopped; returns the peak (MB) per instance. */
export function sampler(vm: string): { stop: () => Promise<Record<string, number>> } {
  const script = `rm -f /tmp/bench-mem.stop /tmp/bench-mem.log
while [ ! -f /tmp/bench-mem.stop ]; do
  for c in $(${containers}); do
    pid=$(nerdctl -n tiffin inspect -f '{{.State.Pid}}' $c 2>/dev/null) || continue
    inst=$(tr '\\0' '\\n' < /proc/$pid/environ | sed -n 's/^TIFFIN_INSTANCE=//p')
    echo "$inst $(cat /sys/fs/cgroup$(awk -F: '$1=="0"{print $3}' /proc/$pid/cgroup)/memory.current)" >> /tmp/bench-mem.log
  done
  sleep 1
done`;
  const child: ChildProcess = spawn("limactl", ["shell", "--workdir", "/", vm, "--", "sudo", "bash", "-c", script], { stdio: "ignore" });
  return {
    async stop() {
      sh(vm, "touch /tmp/bench-mem.stop");
      await new Promise((r) => child.once("exit", r));
      const peak: Record<string, number> = {};
      for (const l of sh(vm, "cat /tmp/bench-mem.log").split("\n")) {
        const [i, b] = l.split(" ");
        if (i !== undefined && b) peak[i] = Math.max(peak[i] ?? 0, Math.round(Number(b) / 1048576));
      }
      return peak;
    },
  };
}
