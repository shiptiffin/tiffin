// The run as Markdown (bench/results/<date>-<label>.md) and JSON beside it.
import type { Check } from "./functional.ts";
import type { LhRow } from "./lighthouse.ts";
import type { LoadRow } from "./load.ts";
import type { Mem } from "./vm.ts";

export type Run = {
  label: string;
  date: string;
  base: string;
  directIp?: string;
  vm?: string;
  notes: string[];
  deploy?: Record<string, string>;
  checks?: Check[];
  lighthouse?: LhRow[];
  load?: LoadRow[];
  memory?: { idle?: Mem[]; after?: Mem[]; peakDuringLoad?: Record<string, number>; boxIdle?: Record<string, number>; boxAfter?: Record<string, number> };
};

const table = (head: string[], rows: (string | number)[][]) =>
  [`| ${head.join(" | ")} |`, `|${head.map(() => "---").join("|")}|`, ...rows.map((r) => `| ${r.join(" | ")} |`)].join("\n");
const esc = (s: string) => s.replaceAll("|", "\\|");
const n0 = (x: number) => (Number.isFinite(x) ? Math.round(x).toString() : "-");

export function markdown(r: Run): string {
  const out: string[] = [`# Showcase bench: ${r.label}`, "", `${r.date}. Target ${r.base}${r.directIp ? `, load via the VM's address ${r.directIp}` : ""}${r.vm ? `, VM ${r.vm}` : ""}.`, ""];
  if (r.deploy) out.push(Object.entries(r.deploy).map(([k, v]) => `- ${k}: ${v}`).join("\n"), "");
  for (const n of r.notes) out.push(`> ${n}`, "");
  if (r.checks) {
    const pass = r.checks.filter((c) => c.ok).length;
    out.push(`## Functional checks: ${pass}/${r.checks.length} pass`, "", table(["", "Check", "Result", "Details"], r.checks.map((c) => [c.id, esc(c.name), c.ok ? "pass" : "**FAIL**", esc(c.detail)])), "");
  }
  if (r.lighthouse?.length) {
    out.push(`## Lighthouse (median of ${Math.max(...r.lighthouse.map((x) => x.runs))} runs)`, "",
      table(["Page", "Form factor", "Score", "FCP ms", "LCP ms", "TBT ms", "CLS", "SI ms", "TTFB ms", "KB"],
        r.lighthouse.map((x) => [x.page, x.formFactor, x.score, n0(x.fcp), n0(x.lcp), n0(x.tbt), x.cls.toFixed(3), n0(x.si), n0(x.ttfb), n0(x.bytes / 1024)])), "");
  }
  if (r.load?.length) {
    out.push("## Load (oha, fixed rate, latency-corrected)", "",
      table(["Target", "Via", "Rate/s", "Got req/s", "2xx %", "p50 ms", "p95 ms", "p99 ms", "max ms", "Errors"],
        r.load.map((x) => [esc(x.name), x.via, x.rate, x.rps, x.ok, x.p50, x.p95, x.p99, x.max, esc(x.errors || (x.ok < 100 ? x.codes : "") || "-")])), "");
  }
  const m = r.memory;
  if (m?.idle || m?.after) {
    out.push("## Memory per instance (MB)", "");
    const rows = (m.idle ?? m.after ?? []).map((i) => {
      const a = m.after?.find((x) => x.instance === i.instance);
      return [i.instance, i.runtime, i.currentMB, i.rssMB, m.peakDuringLoad?.[i.instance] ?? "-", a?.currentMB ?? "-", a?.rssMB ?? "-", a?.peakMB ?? i.peakMB];
    });
    out.push(table(["Instance", "Runtime", "Idle cgroup", "Idle RSS", "Peak during load", "After load cgroup", "After load RSS", "memory.peak"], rows), "");
    if (m.boxIdle || m.boxAfter) {
      const keys = Object.keys(m.boxIdle ?? m.boxAfter ?? {});
      out.push(table(["Box", ...keys], [["idle", ...keys.map((k) => m.boxIdle?.[k] ?? "-")], ["after load", ...keys.map((k) => m.boxAfter?.[k] ?? "-")]]), "");
    }
  }
  return out.join("\n");
}
