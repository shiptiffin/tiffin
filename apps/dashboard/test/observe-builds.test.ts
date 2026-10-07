import { describe, expect, test } from "bun:test";
import { buildBuckets } from "@/components/observe-data";

const H = 3_600_000;
const now = Date.parse("2026-10-07T12:30:00Z");

describe("buildBuckets", () => {
  test("counts a deploy by its current status, so a finished build moves columns", () => {
    const building = [{ status: "building", buildSeconds: 0, createdAt: "2026-10-07T12:05:00Z" }] as const;
    const failed = [{ status: "failed", buildSeconds: 0, createdAt: "2026-10-07T12:05:00Z" }] as const;
    const built = [{ status: "live", buildSeconds: 42, createdAt: "2026-10-07T12:05:00Z" }] as const;
    const sum = (b: ReturnType<typeof buildBuckets>, k: string) => b.reduce((s, x) => s + (x.values[k] ?? 0), 0);

    const a = buildBuckets([...building], now, 24 * H, H);
    expect(sum(a, "ok") + sum(a, "failed")).toBe(0);
    // The same deploy (same id) after it failed, then after it built: the
    // chart once memoised on ids only and kept showing the first answer.
    expect(sum(buildBuckets([...failed], now, 24 * H, H), "failed")).toBe(1);
    expect(sum(buildBuckets([...built], now, 24 * H, H), "ok")).toBe(1);
  });

  test("puts each build in its step and ignores ones outside the span", () => {
    const b = buildBuckets(
      [
        { status: "live", buildSeconds: 10, createdAt: "2026-10-07T12:10:00Z" },
        { status: "superseded", buildSeconds: 10, createdAt: "2026-10-07T11:10:00Z" },
        { status: "failed", buildSeconds: 3, createdAt: "2026-10-05T11:10:00Z" },
      ],
      now,
      24 * H,
      H,
    );
    expect(b).toHaveLength(24);
    expect(b[23].values).toEqual({ ok: 1, failed: 0 });
    expect(b[22].values).toEqual({ ok: 1, failed: 0 });
    expect(b.reduce((s, x) => s + x.values.failed, 0)).toBe(0);
  });
});
