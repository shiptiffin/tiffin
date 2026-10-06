// A believable GET /v1/projects/{p}/usage/history answer for a range, for
// specs (the scratch box has no metrics store): a daily rhythm, a memory
// limit the project nears at its busiest, slow data growth, a few errors.
const steps: Record<string, [number, number]> = { "1h": [3600, 30], "24h": [86400, 600], "7d": [7 * 86400, 3600], "30d": [30 * 86400, 14400] };

export function usageHistory(project: string, range: string, app?: string, now = Date.now()) {
  const [span, step] = steps[range] ?? steps["24h"];
  const end = Math.floor(now / 1000 / step) * step + step;
  const n = span / step;
  const t = Array.from({ length: n }, (_, i) => end - span + i * step);
  let seed = 7;
  const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647);
  const busy = (s: number) => 0.5 + 0.5 * Math.sin(((s / 3600 + 8) / 24) * 2 * Math.PI) ** 2; // a daytime peak
  const series = (f: (s: number, i: number) => number) => t.map((s, i) => [s, Math.round(f(s, i) * 1000) / 1000]);
  const share = app ? 0.6 : 1;
  const out: Record<string, number[][]> = {
    memory: series((s) => share * (300 + 160 * busy(s) + 20 * rnd()) * 1048576),
    cpu: series((s) => share * (6 + 38 * busy(s) + 8 * rnd())),
    requests: series((s) => share * (40 + 260 * busy(s) + 30 * rnd())),
    p50: series((s) => 38 + 20 * busy(s) + 6 * rnd()),
    p95: series((s) => 180 + 140 * busy(s) + 60 * rnd()),
    errors: series((_, i) => (i % 37 === 11 ? 0.02 + 0.03 * rnd() : 0.001 * rnd())),
  };
  if (!app) {
    out.memoryLimit = series(() => 512 * 1048576);
    out.cpuLimit = series(() => 100);
    out.database = series((_, i) => (412 + (i / n) * 18) * 1048576);
    out.connections = series((s) => Math.round(4 + 10 * busy(s) * rnd()));
    out.kv = series((s) => (38 + 14 * busy(s)) * 1048576);
    out.files = series((_, i) => (1.21 + (i / n) * 0.06) * 1073741824);
  }
  return { project, app, range, from: new Date((end - span) * 1000).toISOString(), to: new Date(now).toISOString(), stepSeconds: step, series: out };
}
