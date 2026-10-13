import { describe, expect, test } from "bun:test";
import type { Manifest } from "@/api/client";
import { domainOptionEdit, primaryEdits } from "@/components/domains-primary";
import { applyEdits } from "@/lib/staged";

const base = {
  project: "shop",
  apps: { web: { routes: ["shop", "example.com", "old.com"] }, api: { routes: ["shop-api", "api.example.com", "example.com/api"] } },
  domains: { "old.com": { www: "redirect", primary: true }, "api.example.com": { primary: true } },
} as unknown as Manifest;
const domainsOf = (m: Manifest) => (m as unknown as { domains?: Record<string, unknown> }).domains;

describe("primary domain", () => {
  test("making one primary unsets its app's previous one, keeps www, leaves other apps alone", () => {
    const next = domainsOf(applyEdits(base, primaryEdits(base, "example.com", true)));
    expect(next).toEqual({ "old.com": { www: "redirect" }, "example.com": { primary: true }, "api.example.com": { primary: true } });
  });
  test("stopping drops an entry left with no options", () => {
    const next = domainsOf(applyEdits(base, primaryEdits(base, "api.example.com", false)));
    expect(next).toEqual({ "old.com": { www: "redirect", primary: true } });
  });
  test("the www switch keeps primary", () => {
    const off = domainOptionEdit(base, "old.com", "www", undefined, "w", "u");
    expect(domainsOf(applyEdits(base, [off]))?.["old.com"]).toEqual({ primary: true });
  });
});
