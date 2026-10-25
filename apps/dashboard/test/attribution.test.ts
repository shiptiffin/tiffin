import { expect, test } from "bun:test";
import type { Manifest } from "@/api/client";
import { copiedHash, fromSpec, OFF, privacyHash, privacyText, rememberCopied, withPending } from "@/lib/attribution";

const manifest = (attribution?: object) => ({ project: "shop", services: { analytics: { retentionDays: 365, ...(attribution ? { attribution } : {}) } } }) as unknown as Manifest;

test("settings: listed means on, with campaigns and sign-ups on and no cookie by default", () => {
  expect(fromSpec(undefined)).toEqual(OFF);
  expect(fromSpec({})).toEqual({ on: true, campaigns: true, signups: true, cookie: "off" });
  expect(fromSpec({ signups: false, firstVisitCookie: "ask-eu" })).toEqual({ on: true, campaigns: true, signups: false, cookie: "ask-eu" });
  expect(fromSpec({ firstVisitCookie: "always" }).cookie).toBe("off");
});

test("settings: edits on their way show as the new value", () => {
  const path = ["services", "analytics", "attribution"];
  const on = manifest({ campaigns: true, signups: true, firstVisitCookie: "off" });
  expect(withPending(on, [{ kind: "set", path: [...path, "signups"], from: true, to: false, what: "", undo: "" }]).signups).toBe(false);
  expect(withPending(on, [{ kind: "set", path, from: {}, to: undefined, what: "", undo: "" }]).on).toBe(false);
  expect(withPending(manifest(), [{ kind: "set", path, to: {}, what: "", undo: "" }]).on).toBe(true);
  // Edits elsewhere don't touch it.
  expect(withPending(on, [{ kind: "set", path: ["services", "analytics", "retentionDays"], from: 365, to: 90, what: "", undo: "" }]).on).toBe(true);
});

// The box's own hashes of internal/mod/analytics/privacy.go's text (2026-10-25) for these settings:
// the preview the dashboard shows before a change must be the text the box serves after it.
test("privacy text: the same text as the box writes", async () => {
  expect(await privacyHash(privacyText(OFF))).toBe("87f41909ce98");
  expect(await privacyHash(privacyText({ on: true, campaigns: true, signups: true, cookie: "off" }))).toBe("58a5f7d7d187");
  expect(await privacyHash(privacyText({ on: true, campaigns: true, signups: true, cookie: "outside-eu" }))).toBe("86b08784e637");
  expect(await privacyHash(privacyText({ on: true, campaigns: false, signups: true, cookie: "ask-eu" }, true))).toBe("4865f9ede963");
});

test("privacy text: the cookie and the opt-out link only with the cookie", () => {
  const plain = privacyText({ on: true, campaigns: true, signups: true, cookie: "off" });
  expect(plain).toContain("### Where our customers come from");
  expect(plain).not.toContain("tiffin_first_visit");
  expect(plain).not.toContain("/_tiffin/opt-out");
  const ask = privacyText({ on: true, campaigns: true, signups: true, cookie: "ask-eu" });
  expect(ask).toContain("`tiffin_first_visit`");
  expect(ask).toContain("unless they agree");
  expect(ask).toContain("[this link](/_tiffin/opt-out)");
});

test("the copied hash: remembered per project, and no storage is no error", () => {
  const store = new Map<string, string>();
  (globalThis as { localStorage?: unknown }).localStorage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) };
  expect(copiedHash("shop")).toBeNull();
  rememberCopied("shop", "abc123def456");
  expect(copiedHash("shop")).toBe("abc123def456");
  expect(copiedHash("blog")).toBeNull();
  (globalThis as { localStorage?: unknown }).localStorage = {
    getItem: () => {
      throw new Error("blocked");
    },
    setItem: () => {
      throw new Error("blocked");
    },
  };
  expect(copiedHash("shop")).toBeNull();
  expect(() => rememberCopied("shop", "x")).not.toThrow();
  delete (globalThis as { localStorage?: unknown }).localStorage;
});
