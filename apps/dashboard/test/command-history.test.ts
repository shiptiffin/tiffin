import { beforeEach, expect, test } from "bun:test";

// Web Storage stand-ins (bun has none).
class Mem {
  [k: string]: unknown;
  getItem(k: string) {
    return Object.hasOwn(this, k) ? String(this[k]) : null;
  }
  setItem(k: string, v: string) {
    this[k] = String(v);
  }
  removeItem(k: string) {
    delete this[k];
  }
}
const g = globalThis as Record<string, unknown>;
g.sessionStorage = new Mem();
g.localStorage = new Mem();
const assigned: string[] = [];
g.location = { assign: (u: string) => assigned.push(u), pathname: "/", search: "" };

const { api } = await import("@/api/client");
const { forgetHistory, loadHistory, saveHistory, signOut } = await import("@/lib/command-history");

beforeEach(() => {
  g.sessionStorage = new Mem();
  g.localStorage = new Mem();
});

test("console history stays in this tab, never in localStorage", () => {
  saveHistory("kv", "shop", ["AUTH hunter2"]);
  expect(loadHistory("kv", "shop")).toEqual(["AUTH hunter2"]);
  expect(Object.keys(g.localStorage as object)).toEqual([]);
});

test("signing out forgets every console history, old localStorage ones too", async () => {
  saveHistory("sql", "shop", ["select 'secret'"]);
  (g.localStorage as Mem).setItem("tiffin.kv.history.shop", '["AUTH hunter2"]');
  (g.localStorage as Mem).setItem("tiffin-theme", "dark");
  let out = 0;
  api.logout = async () => void out++;
  await signOut();
  expect(out).toBe(1);
  expect(loadHistory("sql", "shop")).toEqual([]);
  expect((g.localStorage as Mem).getItem("tiffin.kv.history.shop")).toBeNull();
  expect((g.localStorage as Mem).getItem("tiffin-theme")).toBe("dark");
  expect(assigned.at(-1)).toBe("/login?reason=signed-out");
  forgetHistory(); // idempotent
});
