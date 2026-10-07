import { expect, test } from "bun:test";
import { followLogLines, timeKey, type FollowedLine } from "@/lib/log-follow";

class FakeSource {
  static all: FakeSource[] = [];
  onerror: (() => void) | null = null;
  closed = false;
  handlers = new Map<string, (e: MessageEvent) => void>();
  constructor(readonly since: string | undefined) {
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
const line = (time: string, text: string, instance = "web.1"): FollowedLine => ({ time, text, instance, stream: "stdout", deploy: "dep_1" });
const tick = (ms = 5) => new Promise((r) => setTimeout(r, ms));

test("a reconnect resumes after the newest line and never shows a line twice", async () => {
  FakeSource.all = [];
  const got: string[] = [];
  const stop = followLogLines({ open: (s) => new FakeSource(s), since: "2026-10-07T10:00:00Z", onLines: (ls) => got.push(...ls.map((l) => l.text)), batch: 0, retry: 1 });
  const a = FakeSource.all[0];
  expect(a.since).toBe("2026-10-07T10:00:00Z");
  a.emit("log", line("2026-10-07T10:00:01.5Z", "one"));
  a.emit("log", line("2026-10-07T10:00:01.25Z", "two", "web.2")); // another instance, a little older
  a.emit("log", line("2026-10-07T10:00:01.5Z", "one")); // the server's catch-up and live tail overlap
  a.onerror?.();
  await tick();
  const b = FakeSource.all[1];
  expect(a.closed).toBe(true);
  expect(b.since).toBe("2026-10-07T10:00:01.5Z"); // the newest, not the original since (and .5 > .25)
  // The server replays its recent lines first.
  b.emit("log", line("2026-10-07T10:00:01.5Z", "one"));
  b.emit("log", line("2026-10-07T10:00:02Z", "three"));
  b.emit("timeout", { reconnect: true });
  await tick();
  expect(FakeSource.all[2].since).toBe("2026-10-07T10:00:02Z");
  await tick();
  stop();
  expect(got).toEqual(["one", "two", "three"]);
});

test("times compare past the millisecond", () => {
  const [a, b] = [timeKey("2026-10-07T10:00:01.0000005Z"), timeKey("2026-10-07T10:00:01.00000049Z")];
  expect(a[0]).toBe(b[0]);
  expect(a[1]).toBeGreaterThan(b[1]);
});
