const units = ["B", "KB", "MB", "GB", "TB"];

/** 1536 → "1.5 KB". Decimal-ish but binary-based, as file managers show it. */
export function bytes(n: number | null | undefined, digits = 1): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "–";
  if (n < 1024) return `${Math.round(n)} B`;
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(digits).replace(/\.0$/, "")} ${units[i]}`;
}

export function num(n: number | null | undefined): string {
  if (n === null || n === undefined) return "–";
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 1, notation: Math.abs(n) >= 100_000 ? "compact" : "standard" }).format(n);
}

export function pct(ratio: number, digits = 0): string {
  return `${(ratio * 100).toFixed(digits)}%`;
}

export function ms(n: number): string {
  if (n < 1) return "<1 ms";
  if (n < 1000) return `${Math.round(n)} ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)} s`;
  return `${Math.round(n / 60_000)} min`;
}

/** "2h3m" style from seconds. */
export function duration(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d} d ${h} h`;
  if (h > 0) return `${h} h ${m} min`;
  return `${m} min`;
}

/** "*\/10 * * * *" → "every 10 minutes"; anything unusual stays as written. */
export function cronWords(s: string) {
  const f = s.trim().split(/\s+/);
  if (f.length !== 5) return s;
  const [m, h, dom, mon, dow] = f;
  const rest = dom === "*" && mon === "*" && dow === "*";
  if (rest && h === "*" && m === "*") return "every minute";
  if (rest && h === "*" && /^\*\/\d+$/.test(m)) return `every ${m.slice(2)} minutes`;
  if (rest && h === "*" && /^\d+$/.test(m)) return `hourly at :${m.padStart(2, "0")}`;
  if (rest && /^\d+$/.test(h) && /^\d+$/.test(m)) return `daily at ${h.padStart(2, "0")}:${m.padStart(2, "0")} UTC`;
  return s;
}

/**
 * Server status lines are written for terminals ("2 ban(s)", "[22 80 443]", "7m0s ago").
 * This turns the common shapes into plain words without changing what they say.
 */
export function plainWords(s: string): string {
  return s
    .replace(/\b(\d+)((?: [A-Za-z]+)*?) ([A-Za-z]+)\(s\)/g, (_, n: string, mid: string, w: string) => `${n}${mid} ${n === "1" ? w : `${w}s`}`)
    .replace(/\b1 (event|ban|error|job|run)s\b/g, "1 $1")
    .replace(/\[(\d+(?: \d+)+)\]/g, (_, list: string) => list.split(" ").join(", "))
    .replace(/\b(?:(\d+)h)?(\d+)m(\d+(?:\.\d+)?)s\b/g, (_, h?: string, m?: string) => (h && h !== "0" ? `${h} h ${Number(m)} min` : `${Number(m)} min`));
}
