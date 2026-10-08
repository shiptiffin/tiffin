// Checks a managed box's licence (internal/licence): "tl1." + base64url(JSON)
// + "." + base64url(ed25519 signature of everything before the last dot). The
// website only verifies; the cloud worker signs. Both use CLOUD_LICENCE_KEY
// (a 32-byte ed25519 seed); the public half is derived here.
import { createPrivateKey, createPublicKey, verify, type KeyObject } from "node:crypto";

export type Licence = { v: number; box: string; name: string; domain: string; iat: number; gen?: number };

const PKCS8_ED25519 = Buffer.from("302e020100300506032b657004220420", "hex");

/** The public key for a base64 seed, or null when the seed is missing or wrong. */
export function publicKeyFromSeed(seed: string | undefined): KeyObject | null {
  const s = seed?.trim();
  if (!s) return null;
  const raw = Buffer.from(s, s.includes("-") || s.includes("_") ? "base64url" : "base64");
  if (raw.length !== 32) return null;
  const priv = createPrivateKey({ key: Buffer.concat([PKCS8_ED25519, raw]), format: "der", type: "pkcs8" });
  return createPublicKey(priv);
}

/** What a valid token says, or null. */
export function verifyLicence(pub: KeyObject, token: string): Licence | null {
  token = token.trim();
  if (!token.startsWith("tl1.")) return null;
  const i = token.lastIndexOf(".");
  if (i <= 4) return null;
  const body = token.slice(0, i);
  const sig = Buffer.from(token.slice(i + 1), "base64url");
  if (sig.length !== 64) return null;
  try {
    if (!verify(null, Buffer.from(body), pub, sig)) return null;
    const l = JSON.parse(Buffer.from(body.slice(4), "base64url").toString("utf8")) as Licence;
    if (l.v !== 1 || typeof l.box !== "string" || !l.box) return null;
    return l;
  } catch {
    return null;
  }
}
