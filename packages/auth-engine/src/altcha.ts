// ALTCHA proof-of-work captcha (MIT), verified here on the box: no third
// party sees your users. The browser fetches a challenge, spends a fraction
// of a second hashing, and sends the solution in the x-captcha-response
// header (the same header Better Auth's captcha plugin uses). Each solution
// works once.
import type { BetterAuthPlugin } from "better-auth";
import { createAuthEndpoint } from "better-auth/api";
import { CappedMap, createChallenge, randomInt, verifySolution } from "altcha-lib";
import type { Challenge, Solution } from "altcha-lib";
import { deriveKey } from "altcha-lib/algorithms/sha";

/** Paths (under /api/auth) that need a solved challenge. */
export const PROTECTED = [
  "/sign-up/email",
  "/sign-in/email",
  "/sign-in/magic-link",
  "/email-otp/send-verification-otp",
  "/request-password-reset",
];

export const ALGORITHM = "SHA-256";
const TTL_SECONDS = 10 * 60;

export type AltchaOptions = {
  hmacSecret: string;
  /** Counter range. The browser tries counters from 0, so this is the work: ~0.1-1 s. */
  minCounter?: number;
  maxCounter?: number;
};

function decode(header: string): { challenge: Challenge; solution: Solution } | null {
  try {
    const json = header.trim().startsWith("{") ? header : Buffer.from(header, "base64").toString("utf8");
    const v = JSON.parse(json);
    if (v && typeof v === "object" && v.challenge?.parameters && v.solution && typeof v.solution.counter === "number") return v;
  } catch {
    // fall through
  }
  return null;
}

function deny(code: string, message: string, status = 400) {
  return { response: Response.json({ code, message }, { status }) };
}

export const altcha = (o: AltchaOptions) => {
  const hmacSignatureSecret = o.hmacSecret + ":challenge";
  const hmacKeySignatureSecret = o.hmacSecret + ":key";
  const min = o.minCounter ?? 5_000;
  const max = o.maxCounter ?? 40_000;
  // Nonces already used, until they would have expired anyway.
  const used = new CappedMap<string, number>({ maxSize: 50_000 });

  return {
    id: "tiffin-altcha",
    endpoints: {
      altchaChallenge: createAuthEndpoint(
        "/altcha/challenge",
        { method: "GET", metadata: { openapi: { description: "A proof-of-work challenge for sign-up and sign-in." } } },
        async (ctx) => {
          const challenge = await createChallenge({
            algorithm: ALGORITHM,
            cost: 1,
            counter: randomInt(max, min),
            deriveKey,
            expiresAt: Math.floor(Date.now() / 1000) + TTL_SECONDS,
            hmacSignatureSecret,
            hmacKeySignatureSecret,
          });
          ctx.setHeader("Cache-Control", "no-store");
          return ctx.json(challenge);
        },
      ),
    },
    onRequest: async (request, ctx) => {
      if (request.method !== "POST") return;
      const base = ctx.options.basePath ?? "/api/auth";
      let path = new URL(request.url).pathname;
      if (path.startsWith(base)) path = path.slice(base.length);
      path = path.replace(/\/+$/, "") || "/";
      if (!PROTECTED.includes(path)) return;
      const header = request.headers.get("x-captcha-response");
      if (!header) {
        return deny("CAPTCHA_REQUIRED", "This form needs a proof-of-work solution. Fetch GET /api/auth/altcha/challenge, solve it and send it in the x-captcha-response header.");
      }
      const payload = decode(header);
      if (!payload) return deny("CAPTCHA_INVALID", "The x-captcha-response header isn't a valid ALTCHA payload.");
      const nonce = payload.challenge.parameters.nonce;
      if (used.has(nonce)) return deny("CAPTCHA_USED", "This proof-of-work solution was already used. Fetch a new challenge.");
      const r = await verifySolution({
        challenge: payload.challenge,
        solution: payload.solution,
        deriveKey,
        hmacSignatureSecret,
        hmacKeySignatureSecret,
      });
      if (r.expired) return deny("CAPTCHA_EXPIRED", "The proof-of-work challenge expired. Fetch a new one.");
      if (!r.verified) return deny("CAPTCHA_INVALID", "The proof-of-work solution is wrong.");
      // Checked again: concurrent requests with one solution all passed the
      // check above while verification awaited. No await from here to the set.
      if (used.has(nonce)) return deny("CAPTCHA_USED", "This proof-of-work solution was already used. Fetch a new challenge.");
      used.set(nonce, Date.now());
      return;
    },
  } satisfies BetterAuthPlugin;
};
