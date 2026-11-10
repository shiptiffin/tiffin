import { describe, expect, test } from "bun:test";
import { appHosts, dedupe, missingDomains, parseHost } from "../src/hosts";

describe("parseHost", () => {
  test("bare hosts take the default protocol", () => {
    expect(parseHost("Example.COM", "https")).toEqual({ hostname: "example.com", protocol: "https" });
    expect(parseHost("app.example.com.", "http")).toEqual({ hostname: "app.example.com", protocol: "http" });
  });
  test("URLs keep their scheme and drop paths", () => {
    expect(parseHost("http://a.example.com/x?y", "https")).toEqual({ hostname: "a.example.com", protocol: "http" });
  });
  test("ports: default ones dropped, others kept", () => {
    expect(parseHost("https://a.example.com:443", "https")).toEqual({ hostname: "a.example.com", protocol: "https" });
    expect(parseHost("a.example.com:8443", "https")).toEqual({ hostname: "a.example.com", protocol: "https", port: "8443" });
  });
  test("wildcards pass through as Astro patterns", () => {
    expect(parseHost("*.example.com", "https")).toEqual({ hostname: "*.example.com", protocol: "https" });
    expect(parseHost("**.shiptiffin.app", "https")).toEqual({ hostname: "**.shiptiffin.app", protocol: "https" });
  });
  test("refuses what would widen trust or isn't a host", () => {
    for (const bad of ["*", "**", "ftp://x.com", "user@x.com", "x.com:abc", "[::1]", "a b", "", "*.", "a.*.com"]) {
      expect(parseHost(bad, "https")).toBeUndefined();
    }
  });
});

describe("appHosts", () => {
  test("TIFFIN_HOSTS wins, comma or space separated, deduplicated", () => {
    const h = appHosts({ TIFFIN_HOSTS: "example.com, www.example.com\nexample.com  web-p.box.shiptiffin.app", TIFFIN_URL: "https://example.com/" });
    expect(h.source).toBe("TIFFIN_HOSTS");
    expect(h.url).toBe("https://example.com");
    expect(h.domains).toEqual([
      { hostname: "example.com", protocol: "https" },
      { hostname: "www.example.com", protocol: "https" },
      { hostname: "web-p.box.shiptiffin.app", protocol: "https" },
    ]);
  });
  test("bare TIFFIN_HOSTS entries take TIFFIN_URL's scheme", () => {
    expect(appHosts({ TIFFIN_HOSTS: "a.test", TIFFIN_URL: "http://a.test" }).domains).toEqual([{ hostname: "a.test", protocol: "http" }]);
  });
  test("falls back to the host of TIFFIN_URL", () => {
    const h = appHosts({ TIFFIN_URL: "https://web-hello.box.shiptiffin.app" });
    expect(h.source).toBe("TIFFIN_URL");
    expect(h.domains).toEqual([{ hostname: "web-hello.box.shiptiffin.app", protocol: "https" }]);
  });
  test("an empty or junk TIFFIN_HOSTS falls back too", () => {
    expect(appHosts({ TIFFIN_HOSTS: " , * ", TIFFIN_URL: "https://a.test" }).source).toBe("TIFFIN_URL");
  });
  test("nothing set: no hosts", () => {
    expect(appHosts({})).toEqual({ url: undefined, domains: [], source: undefined });
    expect(appHosts({ TIFFIN_URL: "not a url" }).domains).toEqual([]);
  });
});

describe("merging with the project's allowedDomains", () => {
  test("only missing entries are returned (Astro appends arrays)", () => {
    const user = [{ hostname: "example.com", protocol: "https" }, { hostname: "**.example.org" }];
    const ours = [{ hostname: "example.com", protocol: "https" }, { hostname: "web.box.test", protocol: "https" }];
    expect(missingDomains(user, ours)).toEqual([{ hostname: "web.box.test", protocol: "https" }]);
    expect(missingDomains(undefined, ours)).toEqual(ours);
  });
  test("dedupe keeps the first", () => {
    expect(dedupe([{ hostname: "a" }, { hostname: "a" }, { hostname: "a", port: "1" }])).toEqual([{ hostname: "a" }, { hostname: "a", port: "1" }]);
  });
});
