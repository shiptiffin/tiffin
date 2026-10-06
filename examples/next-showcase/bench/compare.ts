// Side by side: node compare.ts results/<a>.json results/<b>.json [...]
import { readFileSync } from "node:fs";
import { basename } from "node:path";
import type { Run } from "./lib/report.ts";

const runs = process.argv.slice(2).map((f) => ({ name: basename(f, ".json"), r: JSON.parse(readFileSync(f, "utf8")) as Run }));
if (runs.length < 2) throw new Error("usage: node compare.ts <run.json> <run.json> [...]");
const names = runs.map((x) => x.r.label);
const row = (cells: (string | number | undefined)[]) => `| ${cells.map((c) => c ?? "-").join(" | ")} |`;
const head = (first: string[]) => [row([...first, ...names]), row([...first, ...names].map(() => "---"))];
const out: string[] = [];

out.push("### Functional checks", "", ...head(["Check"]));
for (const c of runs[0]!.r.checks ?? []) out.push(row([`${c.id} ${c.name}`, ...runs.map((x) => (x.r.checks?.find((y) => y.id === c.id)?.ok ? "pass" : "FAIL"))]));

out.push("", "### Load: p50 / p95 / p99 ms (achieved req/s)", "", ...head(["Target", "Via", "Rate"]));
for (const l of runs[0]!.r.load ?? []) {
  out.push(row([l.name, l.via, l.rate, ...runs.map((x) => {
    const m = x.r.load?.find((y) => y.name === l.name && y.via === l.via);
    return m ? `${m.p50} / ${m.p95} / ${m.p99} (${m.rps}${m.ok < 100 ? `, ${m.ok}% 2xx` : ""})` : undefined;
  })]));
}

out.push("", "### Lighthouse: score, LCP ms, TBT ms", "", ...head(["Page", "Form factor"]));
for (const l of runs[0]!.r.lighthouse ?? []) {
  out.push(row([l.page, l.formFactor, ...runs.map((x) => {
    const m = x.r.lighthouse?.find((y) => y.page === l.page && y.formFactor === l.formFactor);
    return m ? `${m.score}, ${Math.round(m.lcp)}, ${Math.round(m.tbt)}` : undefined;
  })]));
}

out.push("", "### Memory per instance, MB (idle / peak under load / after)", "", ...head(["Instance"]));
for (const i of ["0", "1"]) {
  out.push(row([i, ...runs.map((x) => {
    const m = x.r.memory;
    const idle = m?.idle?.find((y) => y.instance === i);
    return idle ? `${idle.currentMB} / ${m?.peakDuringLoad?.[i] ?? "-"} / ${m?.after?.find((y) => y.instance === i)?.currentMB ?? "-"}` : undefined;
  })]));
}
console.log(out.join("\n"));
