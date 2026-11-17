import { describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { backToBox, boxCallback, consentURL, googleToken, googleVerifier, idTokenEmail, openHandoff, openState, relayApp, sealHandoff, signState, validStart, HANDOFF_TTL, STATE_TTL, SCOPE_SEARCH, SCOPE_VERIFY } from "./google-relay";

const app = relayApp({ GOOGLE_RELAY_CLIENT_ID: "123-relay.apps.googleusercontent.com", GOOGLE_RELAY_CLIENT_SECRET: "relay-secret", SITE_URL: "https://shiptiffin.com/" })!;
const other = relayApp({ GOOGLE_RELAY_CLIENT_ID: "123-relay.apps.googleusercontent.com", GOOGLE_RELAY_CLIENT_SECRET: "another-secret" })!;
const verifier = "v".repeat(64);
const challenge = createHash("sha256").update(verifier).digest("base64url");
const at = new Date("2026-11-17T12:00:00Z");
const later = (s: number) => new Date(at.getTime() + s * 1000);

describe("google relay", () => {
  test("needs both secrets", () => {
    expect(relayApp({ GOOGLE_RELAY_CLIENT_ID: "x" })).toBeNull();
    expect(app.redirect).toBe("https://shiptiffin.com/api/box/google/callback");
  });

  test("takes a box's state and S256 challenge only", () => {
    expect(validStart("tgsc.abcdefghijklmnopqrstuvwx", challenge)).toBe(true);
    expect(validStart("short", challenge)).toBe(false);
    expect(validStart("tgsc.abcdefghijklmnopqrstuvwx", "not-a-hash")).toBe(false);
    expect(validStart("tgsc.abcdefghijklmnop&x=1", challenge)).toBe(false);
  });

  test("goes back only to the box's own dashboard", () => {
    expect(boxCallback("acme.shiptiffin.app")).toBe("https://dashboard.acme.shiptiffin.app/api/auth/callback/google");
    expect(boxCallback("evil.example/x?")).toBeNull();
    expect(boxCallback("acme.shiptiffin.app@evil.example")).toBeNull();
    expect(backToBox({ d: "acme.shiptiffin.app", s: "tgsc.abc" }, { error: "access_denied" })).toBe(
      "https://dashboard.acme.shiptiffin.app/api/auth/callback/google?state=tgsc.abc&error=access_denied",
    );
  });

  test("the state is signed, names the box and runs out", () => {
    const state = signState(app, { b: "box_1", d: "acme.shiptiffin.app", s: "tgsc.abc", c: challenge }, at);
    expect(openState(app, state, at)).toMatchObject({ b: "box_1", d: "acme.shiptiffin.app", s: "tgsc.abc", c: challenge });
    expect(openState(app, state, later(STATE_TTL + 1))).toBeNull();
    expect(openState(other, state, at)).toBeNull();
    const [p, sig] = state.split(".") as [string, string];
    const forged = Buffer.from(JSON.stringify({ ...JSON.parse(Buffer.from(p, "base64url").toString()), d: "evil.example" })).toString("base64url");
    expect(openState(app, `${forged}.${sig}`, at)).toBeNull();
    expect(openState(app, "nonsense", at)).toBeNull();
  });

  test("the consent page asks for both permissions, offline, with PKCE", () => {
    const state = signState(app, { b: "box_1", d: "acme.shiptiffin.app", s: "tgsc.abc", c: challenge }, at);
    const u = new URL(consentURL(app, state));
    const q = u.searchParams;
    expect(u.origin + u.pathname).toBe("https://accounts.google.com/o/oauth2/v2/auth");
    expect(q.get("client_id")).toBe(app.clientId);
    expect(q.get("redirect_uri")).toBe(app.redirect);
    expect(q.get("state")).toBe(state);
    expect(q.get("scope")!.split(" ")).toEqual(expect.arrayContaining(["openid", "email", SCOPE_SEARCH, SCOPE_VERIFY]));
    expect(q.get("access_type")).toBe("offline");
    expect(q.get("prompt")).toBe("consent");
    expect(q.get("code_challenge")).toBe(createHash("sha256").update(googleVerifier(app, state)).digest("base64url"));
    expect(googleVerifier(other, state)).not.toBe(googleVerifier(app, state));
  });

  test("only the box that started collects the grant, within two minutes", () => {
    const code = sealHandoff(app, { b: "box_1", c: challenge, r: "1//refresh", sc: `${SCOPE_SEARCH} ${SCOPE_VERIFY}`, em: "owner@gmail.com" }, at);
    expect(code).not.toContain("refresh");
    expect(openHandoff(app, code, "box_1", verifier, at)).toMatchObject({ r: "1//refresh", em: "owner@gmail.com" });
    expect(openHandoff(app, code, "box_2", verifier, at)).toBeNull(); // another box's licence
    expect(openHandoff(app, code, "box_1", "w".repeat(64), at)).toBeNull(); // not the verifier it started with
    expect(openHandoff(app, code, "box_1", verifier, later(HANDOFF_TTL + 1))).toBeNull();
    expect(openHandoff(other, code, "box_1", verifier, at)).toBeNull();
    expect(openHandoff(app, code.slice(0, -4) + "AAAA", "box_1", verifier, at)).toBeNull();
    expect(openHandoff(app, "", "box_1", verifier, at)).toBeNull();
  });

  test("adds the secret to a grant and passes Google's answer on", async () => {
    let sent = "";
    const fake = (async (_url: unknown, init?: RequestInit) => {
      sent = String(init?.body);
      return Response.json({ error: "invalid_grant", error_description: "Token has been expired or revoked." }, { status: 400 });
    }) as unknown as typeof fetch;
    const r = await googleToken(app, { grant_type: "refresh_token", refresh_token: "1//refresh" }, fake);
    const f = new URLSearchParams(sent);
    expect(f.get("client_secret")).toBe("relay-secret");
    expect(f.get("client_id")).toBe(app.clientId);
    expect(f.get("refresh_token")).toBe("1//refresh");
    expect(r).toEqual({ status: 400, body: { error: "invalid_grant", error_description: "Token has been expired or revoked." } });
  });

  test("reads the address from the ID token", () => {
    const idt = `e30.${Buffer.from(JSON.stringify({ email: "owner@gmail.com" })).toString("base64url")}.sig`;
    expect(idTokenEmail(idt)).toBe("owner@gmail.com");
    expect(idTokenEmail("nonsense")).toBe("");
    expect(idTokenEmail(undefined)).toBe("");
  });
});
