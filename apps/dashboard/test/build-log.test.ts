import { expect, test } from "bun:test";
import { BuildLogModel, MAX_CHARS, MAX_LINE, MAX_LINES } from "@/components/build-log-model";
import { followBuildLog, wholeBuildLog, type BuildLogPage, type BuildLogState } from "@/components/build-log-stream";

const sample = [
  "==> building the image",
  "#5 [build 1/3] RUN bun install",
  "#5 resolve docker.io/library/bun",
  "warning: peer dependency missing",
  "#5 DONE 3.2s",
  "\x1b[31merror: Cannot find module 'x'\x1b[0m",
  "==> starting",
  "listening on :3000",
  "==> live in 7.6s total",
].join("\n");

const shape = (m: BuildLogModel) => ({
  lines: m.lines.map((l) => [l.n, l.text, l.kind, l.group, l.stepSecs]),
  groups: m.groups.map((g) => [g.i, g.title, g.first, g.last, g.errors, g.warns, g.note]),
  errors: m.errors,
  warns: m.warns,
});

test("output parsed chunk by chunk reads the same as all at once", () => {
  const whole = new BuildLogModel();
  whole.push(sample, null);
  whole.end(null);
  for (const size of [1, 3, 7, 50]) {
    const m = new BuildLogModel();
    for (let i = 0; i < sample.length; i += size) m.push(sample.slice(i, i + size), null);
    m.end(null);
    expect(shape(m)).toEqual(shape(whole));
  }
  expect(whole.lines[1].stepSecs).toBe(3.2); // "#5 DONE 3.2s" times the step's first line
  expect(whole.errors).toEqual([5]);
  expect(whole.lines[5].segs[0].color).toBe("var(--danger)");
});

test("endless output keeps only the newest lines, numbered as in the whole log", () => {
  const m = new BuildLogModel();
  const total = MAX_LINES * 2 + 123;
  let chunk = "";
  for (let i = 1; i <= total; i++) {
    chunk += i % 1000 === 0 ? `error: line ${i}\n` : `line ${i}\n`;
    if (chunk.length > 4096) {
      m.push(chunk, null);
      chunk = "";
    }
  }
  m.push(chunk, null);
  expect(m.lines.length).toBeLessThanOrEqual(MAX_LINES);
  expect(m.dropped + m.lines.length).toBe(total);
  expect(m.lines.at(-1)!.n).toBe(total);
  expect(m.lines.at(-1)!.text).toBe(`line ${total}`);
  expect(m.lines[0].n).toBe(m.dropped + 1);
  // The indexes point at what they say.
  for (const i of m.errors) expect(m.lines[i].kind).toBe("error");
  expect(m.groups[0].title).toBe("Earlier output");
  expect(m.groups.at(-1)!.last).toBe(m.lines.length - 1);
});

test("a few huge lines are bounded by characters too", () => {
  const m = new BuildLogModel();
  const line = "x".repeat(MAX_LINE - 1) + "\n";
  for (let i = 0; i < Math.ceil((MAX_CHARS * 2) / MAX_LINE); i++) m.push(line, null);
  const kept = m.lines.reduce((n, l) => n + l.text.length, 0);
  expect(kept).toBeLessThanOrEqual(MAX_CHARS);
  expect(m.dropped).toBeGreaterThan(0);
});

test("one endless line without a newline is cut, not kept whole", () => {
  const m = new BuildLogModel();
  const piece = "y".repeat(100_000);
  for (let i = 0; i < 50; i++) m.push(piece, null); // 5 MB, no newline
  m.push("\nnext\n", null);
  expect(m.lines.length).toBe(2);
  expect(m.lines[0].text.length).toBe(MAX_LINE);
  expect(m.lines[0].cut).toBe(5_000_000 - MAX_LINE);
  expect(m.lines[1].text).toBe("next");
});

// ------------------------------------------------------------------ the reader

/** A log on the "box": pages of at most `page` bytes (characters here). */
function fakeBox(log: string, page: number, done = true) {
  const reads: number[] = [];
  const read = async (offset: number): Promise<BuildLogPage> => {
    reads.push(offset);
    const text = log.slice(offset, offset + page);
    return { text, offset: offset + text.length, done };
  };
  return { read, reads };
}

const settle = async (f: () => boolean) => {
  for (let i = 0; i < 100 && !f(); i++) await new Promise((r) => setTimeout(r, 2));
};

test("a finished deploy's log is read to its end, not just its first page", async () => {
  const log = Array.from({ length: 300 }, (_, i) => `line ${i + 1}`).join("\n") + "\nerror: the real cause\n";
  const box = fakeBox(log, 1000); // like the API's 4 MiB page, smaller
  let last: BuildLogState | undefined;
  const stop = followBuildLog({ read: box.read, stream: () => { throw new Error("a finished log needs no stream"); }, batch: 0 }, (s) => (last = s));
  await settle(() => !!last?.done);
  stop();
  expect(last!.done).toBe(true);
  expect(last!.model.lines.at(-1)!.text).toBe("error: the real cause");
  expect(last!.model.lines.length).toBe(301);
  expect(box.reads.length).toBe(Math.ceil(log.length / 1000) + 1);
});

class FakeSource {
  static all: FakeSource[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  handlers = new Map<string, (e: MessageEvent) => void>();
  constructor(readonly offset: number) {
    FakeSource.all.push(this);
  }
  addEventListener(type: string, f: (e: MessageEvent) => void) {
    this.handlers.set(type, f);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, data: unknown) {
    this.handlers.get(type)?.({ data: JSON.stringify(data) } as MessageEvent);
  }
}

test("a dropped stream resumes from the last offset: nothing twice, nothing lost", async () => {
  FakeSource.all = [];
  const box = fakeBox("first\nsec", 1000, false);
  let last: BuildLogState | undefined;
  const stop = followBuildLog({ read: box.read, stream: (o) => new FakeSource(o), batch: 0, retry: 1 }, (s) => (last = s));
  await settle(() => FakeSource.all.length === 1);
  const a = FakeSource.all[0];
  expect(a.offset).toBe(9);
  a.onopen?.();
  a.emit("log", { text: "ond\nthi", offset: 16 });
  a.onerror?.(); // the connection drops; EventSource would reconnect from offset 9 and replay
  expect(a.closed).toBe(true);
  await settle(() => FakeSource.all.length === 2);
  const b = FakeSource.all[1];
  expect(b.offset).toBe(16);
  b.emit("log", { text: "rd\n", offset: 19 });
  b.emit("timeout", { offset: 19 }); // the server's own time limit: same thing
  await settle(() => FakeSource.all.length === 3);
  const c = FakeSource.all[2];
  expect(c.offset).toBe(19);
  c.emit("log", { text: "fourth", offset: 25 });
  c.emit("done", {});
  await settle(() => !!last?.done);
  stop();
  expect(last!.model.lines.map((l) => l.text)).toEqual(["first", "second", "third", "fourth"]);
}, 10_000);

test("Download reads the whole log from the box", async () => {
  const log = "\x1b[32mok\x1b[0m\r\n".repeat(500);
  const box = fakeBox(log, 333);
  expect(await wholeBuildLog(box.read)).toBe("ok\n".repeat(500));
});
