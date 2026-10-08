// Checks a managed box's licence (internal/licence): "tl1." + base64url(JSON)
// + "." + base64url(ed25519 signature of everything before the last dot). The
// website only verifies, with the public key (CLOUD_LICENCE_PUBLIC); the cloud
// worker signs with the private key, which only its own project holds.
import { createPublicKey, verify, type KeyObject } from "node:crypto";

export type Licence = { v: number; box: string; name: string; domain: string; iat: number; gen?: number };

/** CLOUD_LICENCE_PUBLIC: the base64 of a 32-byte ed25519 public key, or null when unset or wrong. */
export function licencePublicFrom(value: string | undefined): KeyObject | null {
  const s = value?.trim();
  if (!s) return null;
  const raw = Buffer.from(s, s.includes("-") || s.includes("_") ? "base64url" : "base64");
  if (raw.length !== 32) return null;
  try {
    return createPublicKey({ key: { kty: "OKP", crv: "Ed25519", x: raw.toString("base64url") }, format: "jwk" });
  } catch {
    return null;
  }
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
