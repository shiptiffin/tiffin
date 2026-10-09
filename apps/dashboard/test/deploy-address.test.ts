import { describe, expect, test } from "bun:test";
import type { Deploy } from "@/api/modules";
import { appAddress, canRollBack, visitURL } from "@/components/deploy-parts";

const base = { id: "dep_01", project: "shop", app: "web", source: "upload", createdAt: "2026-10-08T10:00:00Z", digest: "sha256:x" } as const;
const d = (o: Partial<Deploy>) => ({ ...base, status: "superseded", ...o }) as Deploy;
const url = "https://d-1a2b3c4d--shop.example.app";

describe("deploy addresses", () => {
  test("a kept version can be visited and made current; a cleaned one neither", () => {
    expect(visitURL(d({ url, retention: "kept" }))).toBe(url);
    expect(canRollBack(d({ url, retention: "kept" }))).toBe(true);
    expect(visitURL(d({ retention: "cleaned" }))).toBeUndefined();
    expect(canRollBack(d({ retention: "cleaned" }))).toBe(false);
  });
  test("the live version is visited at its own address; failed deploys have none", () => {
    expect(visitURL(d({ status: "live", url, retention: "kept" }))).toBe(url);
    expect(visitURL(d({ status: "failed", url }))).toBeUndefined();
  });
  test("a preview is visited while live only", () => {
    expect(visitURL(d({ preview: "pr-1", status: "live", url: "https://pr-1--shop.example.app" }))).toBe("https://pr-1--shop.example.app");
    expect(visitURL(d({ preview: "pr-1", status: "superseded", url: "https://pr-1--shop.example.app" }))).toBeUndefined();
  });
});

describe("the address a new app is shown at", () => {
  test("the app's stable address, not this version's d-<id>-- one", () => {
    expect(appAddress(d({ status: "live", url: "https://d-gjvyb0v3--blog.box.example.app", appUrl: "https://blog.box.example.app" }))).toBe("https://blog.box.example.app");
  });
  test("falls back to the version's address, and to nothing", () => {
    expect(appAddress(d({ status: "live", url }))).toBe(url);
    expect(appAddress(undefined)).toBeUndefined();
  });
});
