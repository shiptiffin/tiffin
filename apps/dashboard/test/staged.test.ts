import { afterEach, beforeEach, expect, test } from "bun:test";
import { api, ApiError, type Manifest, type Plan, type ProjectManifest } from "@/api/client";
import { queryClient } from "@/api/queries";
import { change } from "@/lib/staged";

// A tiny fake box: one project whose manifest has a version, plan/apply that
// behave like the API (plan from the current version; apply re-plans and
// refuses a stale hash with 428). Every call is logged.
type Box = { version: number; manifest: Record<string, unknown>; log: string[]; planDelay: number };
let box: Box;
const real = { plan: api.plan, apply: api.apply, query: queryClient.query.bind(queryClient) };

const planOf = (desired: Manifest): Plan => ({
  project: "shop",
  baseVersion: box.version,
  ops: JSON.stringify(desired) === JSON.stringify(box.manifest) ? [] : [{ kind: "update" } as never],
  risk: "reversible",
  hash: `${box.version}:${JSON.stringify(desired)}`,
  summary: "",
});
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

beforeEach(() => {
  box = { version: 1, manifest: { project: "shop", apps: { web: { memory: 256 } } }, log: [], planDelay: 0 };
  (queryClient as { query: unknown }).query = async () => {
    box.log.push(`read v${box.version}`);
    return { project: "shop", version: box.version, manifest: structuredClone(box.manifest), config: "" } as unknown as ProjectManifest;
  };
  api.plan = async (desired: Manifest) => {
    const p = planOf(desired);
    await sleep(box.planDelay);
    box.log.push(`plan base v${p.baseVersion}`);
    return p;
  };
  api.apply = async (desired: Manifest, confirm?: string) => {
    if (planOf(desired).hash !== confirm) throw new ApiError({ status: 428, code: "confirm", title: "Plan changed" } as never);
    box.manifest = structuredClone(desired) as Record<string, unknown>;
    box.version++;
    box.log.push(`apply -> v${box.version}`);
    return { applied: true } as never;
  };
});
afterEach(() => {
  api.plan = real.plan;
  api.apply = real.apply;
  (queryClient as { query: unknown }).query = real.query;
});

const waitFor = async (f: () => boolean) => {
  for (let i = 0; i < 200 && !f(); i++) await sleep(5);
  expect(f()).toBe(true);
};

test("two changes to one project never revert each other", async () => {
  box.planDelay = 50; // the first change is still planning when the second one fires
  change("shop", { kind: "set", path: ["apps", "web", "sleepAfter"], to: "15m", what: "Let web sleep", undo: "" }, { immediate: true });
  await sleep(10);
  change("shop", { kind: "set", path: ["apps", "web", "memory"], from: 256, to: 512, what: "Give web 512 MB", undo: "" }, { immediate: true });
  await waitFor(() => box.version === 3);
  await sleep(20);
  const web = (box.manifest.apps as Record<string, Record<string, unknown>>).web;
  expect(web).toEqual({ memory: 512, sleepAfter: "15m" });
  // The second change was read after the first one landed, not alongside it.
  expect(box.log).toEqual(["read v1", "plan base v1", "apply -> v2", "read v2", "plan base v2", "apply -> v3"]);
});

test("a manifest read before someone else's change is read again", async () => {
  // Another writer lands between our read and our plan.
  const query = queryClient.query;
  let first = true;
  (queryClient as { query: unknown }).query = async (...a: unknown[]) => {
    const r = await (query as (...x: unknown[]) => Promise<ProjectManifest>)(...a);
    if (first) {
      first = false;
      box.manifest = { ...box.manifest, other: "theirs" };
      box.version++;
    }
    return r;
  };
  change("shop", { kind: "set", path: ["apps", "web", "memory"], from: 256, to: 1024, what: "Give web 1 GB", undo: "" }, { immediate: true });
  await waitFor(() => box.version === 3);
  expect(box.manifest.other).toBe("theirs");
  expect((box.manifest.apps as Record<string, Record<string, unknown>>).web.memory).toBe(1024);
});
