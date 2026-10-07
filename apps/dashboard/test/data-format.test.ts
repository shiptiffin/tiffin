// The table editor's text ⇄ value conversions, in a clock behind UTC so a
// stray zone conversion shows. TZ is set before format.ts loads.
process.env.TZ = "America/Los_Angeles";

import { describe, expect, test } from "bun:test";

const { cellText, fromDraft, inputFor, toDraft } = await import("@/routes/data/format");

const ts = { category: "timestamp", baseType: "timestamp", name: "starts_at", nullable: true };
const tstz = { category: "timestamp", baseType: "timestamptz", name: "created_at", nullable: true };

/** Opening a value in an editor and saving it untouched. */
const roundTrip = (v: string, c: typeof ts) => fromDraft(toDraft(v, c), c);

describe("timestamp without time zone keeps its wall time", () => {
  test("12:00 typed is 12:00 stored, whatever the viewer's clock", () => {
    expect(fromDraft("2026-10-07T12:00", ts)).toBe("2026-10-07T12:00");
    expect(fromDraft("2026-10-07T12:00:30", ts)).toBe("2026-10-07T12:00:30");
  });

  test("shown as stored, not moved to the viewer's clock", () => {
    expect(cellText("2026-10-07 12:00:00")).toBe("2026-10-07 12:00:00");
    expect(toDraft("2026-10-07 12:00:00", ts)).toBe("2026-10-07T12:00:00");
  });

  test("round-trips exactly, fractional seconds included", () => {
    expect(roundTrip("2026-10-07 12:00:00.123456", ts)).toBe("2026-10-07T12:00:00.123456");
    expect(roundTrip("2026-10-07 12:00:00", ts)).toBe("2026-10-07T12:00:00");
    expect(roundTrip("infinity", ts)).toBe("infinity");
    expect(roundTrip("0044-03-15 12:00:00 BC", ts)).toBe("0044-03-15 12:00:00 BC");
  });

  test("a value the picker can't hold is edited as text", () => {
    expect(inputFor(ts, "2026-10-07 12:00:00.123456").type).toBe("text");
    expect(inputFor(ts, "infinity").type).toBe("text");
    expect(inputFor(ts, "2026-10-07 12:00:00.5")).toEqual({ type: "datetime-local", step: 0.001 });
    expect(inputFor(ts, "2026-10-07 12:00:00")).toEqual({ type: "datetime-local", step: 1 });
    expect(inputFor(ts, null)).toEqual({ type: "datetime-local", step: 1 });
  });
});

describe("timestamptz is an instant, edited in the viewer's clock", () => {
  test("12:00 typed in Los Angeles is 19:00 UTC", () => {
    expect(fromDraft("2026-10-07T12:00", tstz)).toBe("2026-10-07T19:00:00Z");
  });

  test("the editor starts in the viewer's clock", () => {
    expect(toDraft("2026-10-07 19:00:00+00", tstz)).toBe("2026-10-07T12:00:00");
  });

  test("round-trips to the same instant, microseconds included", () => {
    expect(roundTrip("2026-10-07 19:00:00.123456+00", tstz)).toBe("2026-10-07T19:00:00.123456Z");
    expect(roundTrip("2026-01-15 08:30:05+00", tstz)).toBe("2026-01-15T08:30:05Z");
    expect(roundTrip("0050-06-01 12:00:00+00", tstz)).toBe("0050-06-01T12:00:00Z");
  });

  test("text naming its own zone goes to Postgres as typed", () => {
    expect(fromDraft("2026-10-07 12:00:00.123456+02", tstz)).toBe("2026-10-07 12:00:00.123456+02");
    expect(fromDraft("now", tstz)).toBe("now");
  });
});
