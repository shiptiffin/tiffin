import { inferLevel } from "@/components/logs-query";

/**
 * A deploy's build log as the viewer reads it: lines with their ANSI colours
 * parsed once, the box's phases as groups, errors and warnings indexed.
 *
 * Built incrementally (each chunk of output parses only its own lines) and
 * bounded, because the output comes from the repository being built: a
 * build that prints without end, or one endless line, must not take the
 * tab down with it. Past MAX_LINES or MAX_CHARS the oldest lines are let go
 * (`dropped` counts them, line numbers stay true), and a line longer than
 * MAX_LINE is cut there (`cut` on the line says by how much). The whole log
 * is always a download away (downloadBuildLog).
 */

export const MAX_LINES = 50_000;
export const MAX_CHARS = 8_000_000;
export const MAX_LINE = 16_384;

// ------------------------------------------------------------------ ANSI

export type Seg = { text: string; color?: string; bold?: boolean; dim?: boolean };

const FG: Record<number, string> = {
  31: "var(--danger)",
  32: "var(--ok)",
  33: "var(--warn-ink)",
  34: "var(--enamel-indigo)",
  35: "var(--enamel-plum)",
  36: "var(--enamel-teal)",
  90: "var(--ink-4)",
  91: "var(--danger)",
  92: "var(--ok)",
  93: "var(--warn-ink)",
  94: "var(--enamel-indigo)",
  95: "var(--enamel-plum)",
  96: "var(--enamel-teal)",
};

// eslint-disable-next-line no-control-regex
const CSI = /\x1b\[([0-9;?]*)([A-Za-z])/g;
// eslint-disable-next-line no-control-regex
const ESC = /\x1b/;

/** Colours and weight from SGR codes; every other escape sequence is dropped. */
export function parseAnsi(raw: string): Seg[] {
  if (!ESC.test(raw)) return [{ text: raw }];
  const out: Seg[] = [];
  let st: Omit<Seg, "text"> = {};
  let at = 0;
  for (const m of raw.matchAll(CSI)) {
    if (m.index! > at) out.push({ text: raw.slice(at, m.index), ...st });
    at = m.index! + m[0].length;
    if (m[2] !== "m") continue;
    const codes = (m[1] || "0").split(";").map(Number);
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i];
      if (c === 0) st = {};
      else if (c === 1) st = { ...st, bold: true };
      else if (c === 2) st = { ...st, dim: true };
      else if (c === 22) st = { ...st, bold: false, dim: false };
      else if (c === 39) st = { ...st, color: undefined };
      else if (c === 38 || c === 48) i += codes[i + 1] === 5 ? 2 : codes[i + 1] === 2 ? 4 : 0;
      else if (FG[c]) st = { ...st, color: FG[c] };
      else if ((c >= 30 && c <= 37) || c === 97) st = { ...st, color: undefined };
    }
  }
  if (at < raw.length) out.push({ text: raw.slice(at), ...st });
  return out.filter((s) => s.text);
}

// eslint-disable-next-line no-control-regex
export const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "").replace(/\x1b\][^\x07]*\x07/g, "");

// ------------------------------------------------------------------ lines

export type Kind = "phase" | "end" | "step" | "quiet" | "error" | "warn" | "plain" | "blank";

/** Errors and warnings by the box's one level rule (inferLevel), as the Logs page and the log store read them. */
export function kindOf(t: string): Kind {
  const s = t.trim();
  if (!s) return "blank";
  const level = inferLevel(t);
  if (level === "error") return "error";
  if (level === "warn") return "warn";
  if (t.startsWith("==>")) return /^==> (built in|live in|release done in)\b/.test(t) ? "end" : "phase";
  if (/^#\d+ (CACHED|DONE [\d.]+s|\.\.\.)$/.test(t) || /^#\d+ (resolve|transferring|sha256:)/.test(t)) return "quiet";
  if (/^#\d+ \[/.test(t)) return "step";
  return "plain";
}

/** Does this (plain) line read as an error, as the viewer paints it? */
export const isErrorLine = (t: string) => kindOf(t) === "error";

/** One line: `n` is its number in the whole log (1-based), `group` the `i` of its group. */
export type Line = { n: number; text: string; segs: Seg[]; kind: Kind; at: number | null; group: number; stepSecs?: number; cut?: number };
/**
 * A phase of the build and the lines under it. `i` is the number of its first
 * line, so it stays the same when older lines are let go. `first` and `last`
 * index `lines` as it is now.
 */
export type Group = { i: number; title: string; first: number; last: number; at: number | null; secs?: number; errors: number; warns: number; /** A phase line with nothing under it: shown as a note, not a step. */ note: boolean };

export class BuildLogModel {
  lines: Line[] = [];
  groups: Group[] = [];
  /** Indexes into lines. */
  errors: number[] = [];
  warns: number[] = [];
  /** Lines let go from the top to stay within the bounds. */
  dropped = 0;
  /** Some line arrived live (so lines carry times). */
  timed = false;
  private chars = 0;
  private partial = "";
  private partialAt: number | null = null;
  private partialCut = 0;
  private stepFirst = new Map<string, Line>();
  private stepDone = new Map<string, number>();

  /** Adds output as it came (any chunking: lines may be split anywhere). */
  push(text: string, at: number | null) {
    let from = 0;
    for (let nl = text.indexOf("\n", from); nl >= 0; nl = text.indexOf("\n", from)) {
      this.take(text.slice(from, nl), at);
      this.emit();
      from = nl + 1;
    }
    if (from < text.length) this.take(text.slice(from), at);
    this.trim();
  }

  /** The output ended: a last line without a newline is a line too. */
  end(at: number | null) {
    if (this.partial || this.partialCut) this.emit(at);
    this.trim();
  }

  /** The text as kept here (ANSI stripped), for Copy. */
  text() {
    return this.lines.map((l) => l.text).join("\n");
  }

  private take(piece: string, at: number | null) {
    if (!this.partial && !this.partialCut) this.partialAt = at;
    const room = MAX_LINE - this.partial.length;
    if (piece.length <= room) this.partial += piece;
    else {
      this.partial += piece.slice(0, Math.max(0, room));
      this.partialCut += piece.length - Math.max(0, room);
    }
  }

  private emit(at?: number | null) {
    const raw = this.partial.replace(/\r/g, "");
    const cut = this.partialCut;
    const when = at !== undefined && this.partialAt === null ? at : this.partialAt;
    this.partial = "";
    this.partialCut = 0;
    this.partialAt = null;
    this.add(raw, when, cut);
  }

  private add(raw: string, at: number | null, cut: number) {
    const idx = this.lines.length;
    const text = stripAnsi(raw);
    const kind = kindOf(text);
    const n = this.dropped + idx + 1;
    if (at !== null) this.timed = true;
    const line: Line = { n, text, segs: parseAnsi(raw), kind, at, group: 0, ...(cut ? { cut } : {}) };
    this.lines.push(line);
    this.chars += text.length;
    this.place(line, idx);
    // A step's time from BuildKit's own "#7 DONE 3.2s", shown on its first line.
    const done = /^#(\d+) DONE ([\d.]+)s/.exec(text);
    if (done) {
      this.stepDone.set(done[1], Number(done[2]));
      const first = this.stepFirst.get(done[1]);
      if (first) first.stepSecs = Number(done[2]);
    }
    if (kind === "step") {
      const id = /^#(\d+)/.exec(text)![1];
      if (!this.stepFirst.has(id)) {
        this.stepFirst.set(id, line);
        line.stepSecs = this.stepDone.get(id);
      }
    }
  }

  /** Files a line under its group and in the error/warning indexes. */
  private place(line: Line, idx: number) {
    let g = this.groups[this.groups.length - 1];
    if (line.kind === "phase" || !g) {
      const next: Group = { i: line.n, title: line.kind === "phase" ? line.text.replace(/^==>\s*/, "") : "Output", first: idx, last: idx, at: line.at, errors: 0, warns: 0, note: line.kind === "phase" };
      // A step's time: from its first line to the next step's, when the lines arrived live.
      if (g && g.at !== null && next.at !== null) g.secs = (next.at - g.at) / 1000;
      this.groups.push(next);
      g = next;
    } else {
      g.note = false;
    }
    g.last = idx;
    line.group = g.i;
    if (line.kind === "error") {
      g.errors++;
      this.errors.push(idx);
    }
    if (line.kind === "warn") {
      g.warns++;
      this.warns.push(idx);
    }
  }

  /** Lets the oldest lines go once over a bound: a fifth at a time, so re-indexing stays rare. */
  private trim() {
    if (this.lines.length <= MAX_LINES && this.chars <= MAX_CHARS) return;
    let k = 0;
    let chars = this.chars;
    const keepLines = Math.floor(MAX_LINES * 0.8);
    const keepChars = Math.floor(MAX_CHARS * 0.8);
    while (k < this.lines.length && (this.lines.length - k > keepLines || chars > keepChars)) chars -= this.lines[k++].text.length;
    // New arrays, not splices: anything still holding the old ones stays consistent.
    const kept = this.lines.slice(k);
    this.dropped += k;
    this.chars = chars;
    this.lines = [];
    this.groups = [];
    this.errors = [];
    this.warns = [];
    for (const l of kept) {
      const idx = this.lines.length;
      this.lines.push(l);
      if (this.groups.length === 0 && l.kind !== "phase") {
        // The top of the log is gone: its lines carry on under a group named for what they were in.
        this.groups.push({ i: l.n, title: "Earlier output", first: idx, last: idx, at: l.at, errors: 0, warns: 0, note: false });
      }
      this.place(l, idx);
    }
    for (const [id, l] of this.stepFirst) if (l.n <= this.dropped) this.stepFirst.delete(id);
  }
}
