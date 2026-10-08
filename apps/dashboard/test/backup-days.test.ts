import { expect, test } from "bun:test";
import type { Backup } from "@/api/modules";
import { dayName, fromLocalInput, groupByDay, inRange, minInput, shortDate, toLocalInput, utcOffset } from "@/lib/backup-days";

// Times are built in local time, so the tests hold in any time zone.
const at = (d: number, h: number, m = 0) => new Date(2026, 9, d, h, m).toISOString();
const set = (id: string, startedAt: string, status = "ok", kind = "incremental", repo = 100): Backup =>
  ({
    id,
    kind,
    trigger: "schedule",
    status,
    startedAt,
    durationMs: 1000,
    postgres: { label: id, type: kind, sizeBytes: 5000, repoBytes: repo },
    valkey: { sizeBytes: 10 },
    platform: { sizeBytes: 1 },
  }) as unknown as Backup;

test("groups a newest-first history by local day", () => {
  const now = new Date(2026, 9, 7, 20, 0);
  const list = [
    set("bk_6", at(7, 18), "running"),
    set("bk_5", at(7, 12)),
    set("bk_4", at(7, 6), "failed"),
    set("bk_3", at(7, 0, 5), "ok", "full", 1000),
    set("bk_2", at(6, 18)),
    set("bk_1", at(1, 0, 5), "ok", "full", 1000),
  ];
  const days = groupByDay(list, now);
  expect(days.map((d) => d.label)).toEqual(["Today", "Yesterday", shortDate(at(1, 0), now)]);
  const today = days[0];
  expect(today.sets.map((b) => b.id)).toEqual(["bk_6", "bk_5", "bk_4", "bk_3"]);
  expect(today.points).toBe(2);
  expect(today.failed).toBe(1);
  expect(today.running).toBe(true);
  // What the successful sets stored: Postgres's delta plus the KV snapshot.
  expect(today.bytes).toBe(110 + 1010);
  expect(days[1]).toMatchObject({ points: 1, failed: 0, running: false, bytes: 110 });
});

test("day names", () => {
  const now = new Date(2026, 9, 7, 9);
  expect(dayName(at(7, 1), now)).toBe("Today");
  expect(dayName(at(6, 23), now)).toBe("Yesterday");
  expect(shortDate(at(7, 1), now)).toBe("Wed 7 Oct");
  expect(shortDate(new Date(2025, 9, 7).toISOString(), now)).toBe("Tue 7 Oct 2025");
});

test("the moment picker's local time round-trips", () => {
  const iso = at(7, 14, 32);
  expect(toLocalInput(iso)).toBe("2026-10-07T14:32");
  expect(fromLocalInput("2026-10-07T14:32")).toBe(iso);
  expect(fromLocalInput("")).toBeNull();
  expect(fromLocalInput("not a time")).toBeNull();
  expect(utcOffset(iso)).toMatch(/^UTC([+−]\d{1,2}(:\d\d)?)?$/);
});

test("the restorable range bounds the picker", () => {
  const r = { earliest: new Date(2026, 9, 1, 0, 30, 15).toISOString(), latest: at(7, 18) };
  expect(minInput(r.earliest)).toBe("2026-10-01T00:31");
  expect(inRange(fromLocalInput("2026-10-01T00:31"), r)).toBe(true);
  expect(inRange(fromLocalInput("2026-10-01T00:30"), r)).toBe(false);
  expect(inRange(fromLocalInput("2026-10-07T18:00"), r)).toBe(true);
  expect(inRange(fromLocalInput("2026-10-07T18:01"), r)).toBe(false);
  expect(inRange(null, r)).toBe(false);
  expect(inRange(at(5, 5), null)).toBe(false);
});
