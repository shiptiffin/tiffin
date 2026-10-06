// A stand-in for the auth engine: signs session cookie caches the way it
// does (EdDSA, Better Auth's session-cache JWT), serves its JWKS and answers
// /tiffin/session, counting calls.

const b64u = (b: Uint8Array | string) => Buffer.from(b).toString("base64url");

let engines = 0;

export type FakeOrg = { id: string; name: string; slug: string; role: string; memberRole: string } | null;

export async function fakeEngine(answer: unknown = null) {
  const keys = (await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"])) as CryptoKeyPair;
  const x = (await crypto.subtle.exportKey("jwk", keys.publicKey)).x!;
  const calls: { url: string; headers: Headers }[] = [];
  const engine = {
    // Its own address, so each fake gets its own JWKS cache entry.
    url: `http://engine-${++engines}/api/auth`,
    calls,
    answer,
    setCookie: [] as string[],
    sessionCalls: () => calls.filter((c) => c.url.includes("/tiffin/session")).length,
    fetch: (async (url: string, init?: RequestInit) => {
      calls.push({ url, headers: new Headers(init?.headers) });
      if (url.endsWith("/jwks")) return Response.json({ keys: [{ kid: "k1", kty: "OKP", crv: "Ed25519", x, alg: "EdDSA" }] });
      const h = new Headers({ "content-type": "application/json" });
      for (const c of engine.setCookie) h.append("set-cookie", c);
      return new Response(JSON.stringify(engine.answer), { headers: h });
    }) as unknown as typeof fetch,
    /** A session_data cookie value for session token `token`. */
    async jwt(token: string, o: { org?: FakeOrg; ttl?: number; kid?: string; sid?: string } = {}) {
      const now = Math.floor(Date.now() / 1000);
      const header = b64u(JSON.stringify({ alg: "EdDSA", kid: o.kid ?? "k1", typ: "better-auth.session-cache+jwt" }));
      const payload = b64u(
        JSON.stringify({
          session: { id: "s1", token, userId: "u1", expiresAt: new Date(Date.now() + 86_400_000).toISOString(), activeOrganizationId: o.org === undefined ? "o1" : (o.org?.id ?? null), ipAddress: "10.0.0.1" },
          user: { id: "u1", email: "ana@example.com", name: "Ana", image: null, emailVerified: true, createdAt: "2026-01-01T00:00:00.000Z", updatedAt: "2026-01-01T00:00:00.000Z", banned: false },
          updatedAt: Date.now(),
          version: "1",
          tiffin: { organization: o.org === undefined ? { id: "o1", name: "Acme", slug: "acme", role: "owner", memberRole: "owner" } : o.org },
          sid: o.sid ?? token,
          iat: now,
          exp: now + (o.ttl ?? 60),
          iss: "https://shop.example.com/api/auth",
          aud: "better-auth:session-cache",
          sub: "u1",
        }),
      );
      const sig = new Uint8Array(await crypto.subtle.sign({ name: "Ed25519" }, keys.privateKey, new TextEncoder().encode(`${header}.${payload}`)));
      return `${header}.${payload}.${b64u(sig)}`;
    },
  };
  return engine;
}
