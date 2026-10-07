// One project's bad settings never take the others' sign-in down.
import { expect, test } from "bun:test";
import { parseConfig } from "../src/config";
import { projectConfig } from "./helpers";

test("an invalid project (or proxy) is left out, the rest of the file is served", () => {
  const good = projectConfig("postgres://x/shop");
  const bad = projectConfig("postgres://x/evil", {
    hosts: ["evil.tiffin.localhost"],
    methods: ["oidc"],
    // An OpenID Connect issuer set through the project's secrets that isn't a URL.
    social: { oidc: { clientId: "c", clientSecret: "s", issuer: "not-a-url", proxied: false } },
  });
  const warned: string[] = [];
  const c = parseConfig(
    { version: 1, listen: [], projects: { shop: good, evil: bad, "Bad Name": good }, proxy: { url: "nope", host: "x", secret: "short", social: {} } },
    (msg, f) => warned.push(`${msg} ${JSON.stringify(f)}`),
  );
  expect(Object.keys(c.projects)).toEqual(["shop"]);
  expect(c.projects.shop!.appName).toBe("Shop");
  expect(c.proxy).toBeUndefined();
  expect(warned.join("\n")).toContain('"project":"evil"');
  expect(warned.join("\n")).toContain("social.oidc.issuer");
  expect(warned).toHaveLength(3);
});

test("a malformed file as a whole is still refused", () => {
  expect(() => parseConfig({ version: 2, projects: {} })).toThrow(/auth config invalid/);
});
