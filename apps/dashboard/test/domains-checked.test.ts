import { describe, expect, test } from "bun:test";
import { checkedLine } from "@/components/domains-setup";

describe("domain check line", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  const at = (s: number) => new Date(now + s * 1000).toISOString();
  test("when the box looked and when it looks next", () => {
    expect(checkedLine(at(-120), at(30), now)).toBe("Checked 2 minutes ago · next check in 30 s");
    expect(checkedLine(at(-1), at(90 * 60), now)).toBe("Checked just now · next check in 1 h 30 min");
    expect(checkedLine(at(-20), at(-1), now)).toBe("Checked 20 seconds ago · checking again now");
    expect(checkedLine(undefined, undefined, now)).toBe("Not checked yet");
  });
});
