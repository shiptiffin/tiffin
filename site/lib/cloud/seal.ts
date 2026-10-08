// Seals customers' Hetzner tokens to the cloud worker's X25519 public key
// (CLOUD_SEAL_PUBLIC), in the format the worker opens (internal/cloud/seal.go).
// The website can seal but never open: only the worker holds the private key,
// in its own project's secrets. Each value gets a fresh ephemeral key; the
// additional data binds it to its row ("hetzner:<box id>").
//
//   "v2." + base64url(ephemeral public key) + "." + base64url(nonce | AES-256-GCM(key, value, aad) | tag)
//   key = HKDF-SHA256(X25519(ephemeral, worker), salt = ephemeral pub | worker pub, info = "shiptiffin seal v2")
import { createCipheriv, createHash, createPublicKey, diffieHellman, generateKeyPairSync, hkdfSync, randomBytes, type KeyObject } from "node:crypto";

const b64 = (b: Buffer) => b.toString("base64url");
export const SEAL_INFO = "shiptiffin seal v2";

/** CLOUD_SEAL_PUBLIC: the base64 of a 32-byte X25519 public key, or null when unset or wrong. */
export function sealPublicFrom(value: string | undefined): KeyObject | null {
  const s = value?.trim();
  if (!s) return null;
  const raw = Buffer.from(s, s.includes("-") || s.includes("_") ? "base64url" : "base64");
  if (raw.length !== 32) return null;
  try {
    return createPublicKey({ key: { kty: "OKP", crv: "X25519", x: raw.toString("base64url") }, format: "jwk" });
  } catch {
    return null;
  }
}

/** The raw 32 bytes of an X25519 key object. */
export function rawX25519(k: KeyObject): Buffer {
  return Buffer.from(k.export({ format: "jwk" }).x as string, "base64url");
}

export function sealKey(shared: Buffer, ephPub: Buffer, recipPub: Buffer): Buffer {
  return Buffer.from(hkdfSync("sha256", shared, Buffer.concat([ephPub, recipPub]), SEAL_INFO, 32));
}

/** Seals value for the worker (pub), bound to the row aad names. */
export function seal(pub: KeyObject, value: string, aad: string): string {
  const eph = generateKeyPairSync("x25519");
  const shared = diffieHellman({ privateKey: eph.privateKey, publicKey: pub });
  const ephPub = rawX25519(eph.publicKey);
  const key = sealKey(shared, ephPub, rawX25519(pub));
  shared.fill(0);
  try {
    const nonce = randomBytes(12);
    const c = createCipheriv("aes-256-gcm", key, nonce);
    c.setAAD(Buffer.from(aad, "utf8"));
    const body = Buffer.concat([nonce, c.update(Buffer.from(value, "utf8")), c.final(), c.getAuthTag()]);
    return `v2.${b64(ephPub)}.${b64(body)}`;
  } finally {
    key.fill(0);
  }
}

export const tokenAAD = (boxId: string) => `hetzner:${boxId}`;

/** What stays of a forgotten token: 12 hex characters of its sha256. */
export function fingerprint(token: string): string {
  return createHash("sha256").update(token.trim()).digest("hex").slice(0, 12);
}
