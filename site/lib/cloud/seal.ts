// Envelope encryption for customers' Hetzner tokens, the same format the
// cloud worker opens (internal/cloud/seal.go): a random data key per value,
// AES-256-GCM, the data key wrapped with CLOUD_KEK (a project secret, never in
// the database), bound to its row by the additional data ("hetzner:<box id>").
//
//   "v1." + base64url(nonce | AES-GCM(KEK, data key) | tag) + "." +
//           base64url(nonce | AES-GCM(data key, value, aad) | tag)
import { createCipheriv, createDecipheriv, createHash, randomBytes } from "node:crypto";

const b64 = (b: Buffer) => b.toString("base64url");

/** CLOUD_KEK: the base64 of 32 random bytes, or null when unset or wrong. */
export function kekFrom(value: string | undefined): Buffer | null {
  const s = value?.trim();
  if (!s) return null;
  const raw = Buffer.from(s, s.includes("-") || s.includes("_") ? "base64url" : "base64");
  return raw.length === 32 ? raw : null;
}

function gcmSeal(key: Buffer, plain: Buffer, aad: Buffer): Buffer {
  const nonce = randomBytes(12);
  const c = createCipheriv("aes-256-gcm", key, nonce);
  c.setAAD(aad);
  const body = Buffer.concat([c.update(plain), c.final()]);
  return Buffer.concat([nonce, body, c.getAuthTag()]);
}

function gcmOpen(key: Buffer, sealed: Buffer, aad: Buffer): Buffer {
  if (sealed.length < 12 + 16) throw new Error("too short");
  const d = createDecipheriv("aes-256-gcm", key, sealed.subarray(0, 12));
  d.setAAD(aad);
  d.setAuthTag(sealed.subarray(sealed.length - 16));
  return Buffer.concat([d.update(sealed.subarray(12, sealed.length - 16)), d.final()]);
}

export function seal(kek: Buffer, value: string, aad: string): string {
  const dek = randomBytes(32);
  try {
    const wrapped = gcmSeal(kek, dek, Buffer.from("dek"));
    const body = gcmSeal(dek, Buffer.from(value, "utf8"), Buffer.from(aad, "utf8"));
    return `v1.${b64(wrapped)}.${b64(body)}`;
  } finally {
    dek.fill(0);
  }
}

/** The value, or null when it does not open (another KEK, another row, changed). */
export function open(kek: Buffer, sealed: string, aad: string): string | null {
  const m = /^v1\.([A-Za-z0-9_-]+)\.([A-Za-z0-9_-]+)$/.exec(sealed);
  if (!m) return null;
  try {
    const dek = gcmOpen(kek, Buffer.from(m[1]!, "base64url"), Buffer.from("dek"));
    try {
      return gcmOpen(dek, Buffer.from(m[2]!, "base64url"), Buffer.from(aad, "utf8")).toString("utf8");
    } finally {
      dek.fill(0);
    }
  } catch {
    return null;
  }
}

export const tokenAAD = (boxId: string) => `hetzner:${boxId}`;
export const ownerAAD = (boxId: string) => `owner:${boxId}`;

/** What stays of a forgotten token: 12 hex characters of its sha256. */
export function fingerprint(token: string): string {
  return createHash("sha256").update(token.trim()).digest("hex").slice(0, 12);
}
