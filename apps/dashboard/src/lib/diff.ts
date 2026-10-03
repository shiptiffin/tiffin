// A small line diff for config files (tens of lines): LCS, then hunks with
// a few lines of context and the rest folded ("4 unchanged lines").

export type DiffLine = { k: "same" | "add" | "del"; text: string; a?: number; b?: number };
export type DiffRow = DiffLine | { k: "fold"; count: number };

export function diffLines(before: string, after: string): DiffLine[] {
  const a = before.replace(/\n$/, "").split("\n");
  const b = after.replace(/\n$/, "").split("\n");
  const n = a.length;
  const m = b.length;
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ k: "same", text: a[i], a: i + 1, b: j + 1 });
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ k: "del", text: a[i], a: i + 1 });
      i++;
    } else {
      out.push({ k: "add", text: b[j], b: j + 1 });
      j++;
    }
  }
  while (i < n) out.push({ k: "del", text: a[i], a: ++i });
  while (j < m) out.push({ k: "add", text: b[j], b: ++j });
  return out;
}

/** Keeps `context` unchanged lines around each change and folds the rest. */
export function hunks(lines: DiffLine[], context = 3): DiffRow[] {
  const keep = lines.map(() => false);
  lines.forEach((l, i) => {
    if (l.k === "same") return;
    for (let d = -context; d <= context; d++) if (lines[i + d]) keep[i + d] = true;
  });
  const out: DiffRow[] = [];
  let folded = 0;
  lines.forEach((l, i) => {
    if (keep[i]) {
      if (folded) out.push({ k: "fold", count: folded });
      folded = 0;
      out.push(l);
    } else folded++;
  });
  if (folded) out.push({ k: "fold", count: folded });
  return out;
}

export function diffCounts(lines: DiffLine[]) {
  return { add: lines.filter((l) => l.k === "add").length, del: lines.filter((l) => l.k === "del").length };
}

/** The config text without its leading comment (the "pulled from the box" note), so only real edits differ. */
export function stripNote(config: string): string {
  return config
    .split("\n")
    .filter((l) => !l.startsWith("// "))
    .join("\n");
}
