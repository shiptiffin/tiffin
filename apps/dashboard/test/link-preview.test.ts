import { describe, expect, test } from "bun:test";
import type { LinkPreview } from "@/api/modules";
import { changeWords, comparedWords, crumb, hostOf, shown, summaryWords, tagsText } from "@/lib/link-preview";

const report = (o: Partial<LinkPreview> = {}): LinkPreview => ({
  deploy: "dep_01",
  checkedAt: "2026-10-26T10:00:00Z",
  url: "https://www.dibs.page/",
  status: "ok",
  checks: [],
  canCheck: true,
  ...o,
});

describe("the link preview in a few words", () => {
  test("fine, warnings, problems, unreadable", () => {
    expect(summaryWords({ status: "ok" })).toBe("All good");
    expect(summaryWords({ status: "warn", warn: 1 })).toBe("1 warning");
    expect(summaryWords({ status: "warn", warn: 2 })).toBe("2 warnings");
    expect(summaryWords({ status: "bad", bad: 1 })).toBe("1 problem");
    expect(summaryWords({ status: "bad", bad: 1, warn: 2 })).toBe("1 problem, 2 warnings");
    expect(summaryWords({ status: "unreadable" })).toBe("Couldn’t read the page");
  });
});

describe("what each surface shows", () => {
  test("search uses the page's own title; apps use og: first", () => {
    const s = shown(report({ pageTitle: "Dibs", metaDescription: "Sign-up sheets", title: "Dibs – sign-up sheets", description: "Ad-free", twitterCard: "summary_large_image" }));
    expect(s.searchTitle).toBe("Dibs");
    expect(s.searchText).toBe("Sign-up sheets");
    expect(s.title).toBe("Dibs – sign-up sheets");
    expect(s.text).toBe("Ad-free");
    expect(s.host).toBe("dibs.page");
    expect(s.site).toBe("dibs.page");
    expect(s.large).toBe(true);
  });
  test("a page with nothing shows its address", () => {
    const s = shown(report());
    expect(s.searchTitle).toBe("dibs.page");
    expect(s.title).toBe("dibs.page");
    expect(s.large).toBe(false);
  });
  test("Google's crumb", () => {
    expect(crumb("https://dibs.page/")).toBe("https://dibs.page");
    expect(crumb("https://dibs.page/blog/launch")).toBe("https://dibs.page › blog › launch");
    expect(hostOf("not a url")).toBe("not a url");
  });
});

describe("changes", () => {
  test("fields, added, removed and checks in words", () => {
    expect(changeWords({ field: "Title", from: "Old", to: "New" })).toBe("Title: Old → New");
    expect(changeWords({ field: "Share image", from: "https://x.com/og.png" })).toBe("Share image removed (was https://x.com/og.png)");
    expect(changeWords({ field: "X card", to: "summary_large_image" })).toBe("X card added: summary_large_image");
    expect(changeWords({ field: "Image size", from: "ok", to: "bad", worse: true })).toBe("Image size: fine → a problem");
  });
  test("what they're against", () => {
    expect(comparedWords(report())).toBeUndefined();
    expect(comparedWords(report({ compared: { deploy: "dep_00", version: 12 } }))).toBe("Changed since v12");
    expect(comparedWords(report({ compared: { deploy: "dep_00", version: 12, production: true } }))).toBe("Differs from production (v12)");
  });
  test("tags copy as name, tab, value lines", () => {
    expect(tagsText([{ name: "og:title", value: "Dibs" }, { name: "title", value: "Dibs" }])).toBe("og:title\tDibs\ntitle\tDibs");
  });
});
