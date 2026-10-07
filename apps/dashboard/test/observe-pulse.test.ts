import { describe, expect, test } from "bun:test";
import { deploysQuery, runtimeQuery } from "@/lib/pulse";

type Interval = (q: { state: { data?: Array<{ status: string }> } }) => number | false;
const every = (o: { refetchInterval?: unknown }, data?: Array<{ status: string }>) => (o.refetchInterval as Interval)({ state: { data } });

describe("deploy history polling", () => {
  test("shares one key with the prefix the deploy actions invalidate", () => {
    expect(deploysQuery("shop", "web").queryKey.slice(0, 3)).toEqual(["deploys", "shop", "web"]);
  });

  test("polls fast only while a deploy is in flight, and not at all out of view", () => {
    expect(every(deploysQuery("shop", "web"), [{ status: "building" }, { status: "live" }])).toBe(5_000);
    expect(every(deploysQuery("shop", "web"), [{ status: "live" }, { status: "superseded" }])).toBe(30_000);
    expect(every(deploysQuery("shop", "web"), undefined)).toBe(30_000);
    expect(every(deploysQuery("shop", "web", false), [{ status: "building" }])).toBe(false);
    expect(runtimeQuery("shop", "web", false).refetchInterval).toBe(false);
  });
});
