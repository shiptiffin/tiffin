// The owner's SSH public keys, as /start takes them (pasted, from GitHub or
// from the Hetzner project) and as createBox checks them again before the
// setup job gets them. The same rules as the box's own parser
// (internal/sshkeys): one key per line, no options, no certificates, no
// private keys, ed25519 / ecdsa / security-key variants, RSA of 3072 bits or
// more, at most 10 keys and 16 KB. The worker checks again with Go's parser
// before it touches Hetzner; this one gives the fast, plain answer.
// Runs in the browser and on the server (atob, crypto.subtle): no Node APIs.

export const MAX_KEYS = 10;
export const MAX_BYTES = 16 * 1024;
export const MIN_RSA_BITS = 3072;

/** The key types accepted, by their wire name. */
export const KEY_TYPES = [
  "ssh-ed25519",
  "sk-ssh-ed25519@openssh.com",
  "ecdsa-sha2-nistp256",
  "ecdsa-sha2-nistp384",
  "ecdsa-sha2-nistp521",
  "sk-ecdsa-sha2-nistp256@openssh.com",
  "ssh-rsa",
] as const;
export type KeyType = (typeof KEY_TYPES)[number];

const ACCEPTED = new Set<string>(KEY_TYPES);

/** Where a key came from, as the setup job and the box record it. */
export type KeySource = "pasted" | "hetzner" | `github:${string}`;

/** GitHub's rule for usernames (and the box's): letters, digits and single hyphens inside, at most 39. */
export const GITHUB_USER = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$/;

export class KeyError extends Error {}

export type PublicKey = {
  type: KeyType;
  /** The key's base64, exactly as it was given (and re-checked). */
  base64: string;
  /** "type base64": the line without its comment, which is what the setup job gets. */
  line: string;
  /** The comment after the key, if any (shown to the owner, never sent on). */
  comment: string;
  /** RSA modulus size; ecdsa and ed25519 sizes are fixed by the type. */
  bits?: number;
  blob: Uint8Array;
};

function decode(b64: string): Uint8Array | null {
  if (!/^[A-Za-z0-9+/]+={0,2}$/.test(b64) || b64.length % 4 !== 0) return null;
  try {
    const s = atob(b64);
    const out = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

/** Reads the SSH wire format: uint32 length, then that many bytes. */
class Reader {
  private at = 0;
  constructor(private readonly b: Uint8Array) {}
  bytes(): Uint8Array {
    if (this.at + 4 > this.b.length) throw new KeyError("not an SSH public key");
    const n = ((this.b[this.at]! << 24) | (this.b[this.at + 1]! << 16) | (this.b[this.at + 2]! << 8) | this.b[this.at + 3]!) >>> 0;
    this.at += 4;
    if (n > this.b.length - this.at) throw new KeyError("not an SSH public key");
    const out = this.b.subarray(this.at, this.at + n);
    this.at += n;
    return out;
  }
  text(): string {
    const b = this.bytes();
    let s = "";
    for (const c of b) {
      if (c < 0x20 || c > 0x7e) throw new KeyError("not an SSH public key");
      s += String.fromCharCode(c);
    }
    return s;
  }
  done() {
    if (this.at !== this.b.length) throw new KeyError("not an SSH public key");
  }
}

/** Bits in an mpint (big-endian, maybe with a leading zero byte for the sign). */
function mpintBits(b: Uint8Array): number {
  let i = 0;
  while (i < b.length && b[i] === 0) i++;
  if (i === b.length) return 0;
  return (b.length - i - 1) * 8 + (32 - Math.clz32(b[i]!));
}

const CURVE_POINT: Record<string, number> = { nistp256: 65, nistp384: 97, nistp521: 133 };

/** Checks the decoded key against its declared type; the RSA size for RSA. */
function checkBlob(type: KeyType, blob: Uint8Array): { bits?: number } {
  const r = new Reader(blob);
  if (r.text() !== type) throw new KeyError("the key's type doesn't match its data (was it edited?)");
  switch (type) {
    case "ssh-ed25519":
    case "sk-ssh-ed25519@openssh.com": {
      if (r.bytes().length !== 32) throw new KeyError("not an SSH public key");
      if (type.startsWith("sk-") && !r.text().length) throw new KeyError("not an SSH public key");
      r.done();
      return {};
    }
    case "ecdsa-sha2-nistp256":
    case "ecdsa-sha2-nistp384":
    case "ecdsa-sha2-nistp521":
    case "sk-ecdsa-sha2-nistp256@openssh.com": {
      const curve = /nistp\d+/.exec(type)![0];
      if (r.text() !== curve) throw new KeyError("the key's curve doesn't match its type");
      const q = r.bytes();
      if (q.length !== CURVE_POINT[curve] || q[0] !== 4) throw new KeyError("not an SSH public key");
      if (type.startsWith("sk-") && !r.text().length) throw new KeyError("not an SSH public key");
      r.done();
      return {};
    }
    case "ssh-rsa": {
      const e = r.bytes();
      const n = r.bytes();
      r.done();
      if (mpintBits(e) < 2 || mpintBits(e) > 64 || (e[e.length - 1]! & 1) === 0) throw new KeyError("not an SSH public key");
      const bits = mpintBits(n);
      if (bits < MIN_RSA_BITS) throw new KeyError(`RSA keys need at least ${MIN_RSA_BITS} bits (this one has ${bits}); ed25519 is better: ssh-keygen -t ed25519`);
      return { bits };
    }
  }
}

/** Looks like a key type (a second key on the line, or one we don't take). */
const typeLike = (s: string) => ACCEPTED.has(s) || /^(ssh|ecdsa|sk)-/.test(s);

/** One line: "type base64 [comment]". Throws KeyError with a plain reason. */
export function parseLine(raw: string): PublicKey {
  const line = raw.trim();
  if (line.startsWith("-----") || line.includes("PRIVATE KEY")) throw new KeyError("that's a private key; paste the public one (the .pub file)");
  const [type = "", b64 = "", ...rest] = line.split(/\s+/);
  if (!typeLike(type)) {
    // `from="…",no-pty ssh-ed25519 AAAA…`: options before the key.
    if (/[=,"]/.test(type) || /^(no-|restrict|cert-authority|permit|principals|command|environment|expiry-time|tunnel|from|verify-required)/.test(type)) {
      throw new KeyError("options before the key (like command= or from=) aren't allowed");
    }
    throw new KeyError("not an SSH public key");
  }
  if (type.includes("-cert-v01@")) throw new KeyError("certificates aren't supported; use the plain public key");
  if (!ACCEPTED.has(type)) throw new KeyError(`${type} keys aren't accepted; use ed25519 (ssh-keygen -t ed25519)`);
  if (rest.some(typeLike)) throw new KeyError("one key per line");
  const blob = decode(b64);
  if (!blob) throw new KeyError("not an SSH public key");
  const { bits } = checkBlob(type as KeyType, blob);
  return { type: type as KeyType, base64: b64, line: `${type} ${b64}`, comment: rest.join(" "), bits, blob };
}

/** What a person pasted: one key per non-blank line, duplicates dropped. Throws KeyError naming the line. */
export function parseKeys(text: string): PublicKey[] {
  if (text.length > MAX_BYTES) throw new KeyError(`that's more than ${MAX_BYTES / 1024} KB of keys`);
  const out: PublicKey[] = [];
  const seen = new Set<string>();
  const lines = text.split("\n");
  lines.forEach((raw, i) => {
    const l = raw.replace(/\r$/, "").trim();
    if (!l) return;
    let k: PublicKey;
    try {
      k = parseLine(l);
    } catch (e) {
      if (e instanceof KeyError && lines.filter((x) => x.trim()).length > 1) throw new KeyError(`line ${i + 1}: ${e.message}`);
      throw e;
    }
    if (seen.has(k.base64)) return;
    seen.add(k.base64);
    out.push(k);
  });
  if (out.length === 0) throw new KeyError("no SSH public key found");
  if (out.length > MAX_KEYS) throw new KeyError(`at most ${MAX_KEYS} keys`);
  return out;
}

/** "SHA256:…", as ssh-keygen -l prints it: the key's data hashed, base64 without padding. */
export async function fingerprint(k: Pick<PublicKey, "blob">): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", k.blob as Uint8Array<ArrayBuffer>));
  let s = "";
  for (const c of d) s += String.fromCharCode(c);
  return "SHA256:" + btoa(s).replace(/=+$/, "");
}

/** "SHA256:7oJ928uk…aU96zY": enough to tell keys apart at a glance. */
export const shortFingerprint = (fp: string) => {
  const h = fp.replace(/^SHA256:/, "");
  return h.length > 18 ? `SHA256:${h.slice(0, 8)}…${h.slice(-6)}` : fp;
};

/** "ED25519", "ECDSA P-256", "RSA 4096", "ED25519 security key". */
export function keyKind(type: string, bits?: number | null): string {
  const sk = type.startsWith("sk-") ? " security key" : "";
  if (type.includes("ed25519")) return `ED25519${sk}`;
  const curve = /nistp(\d+)/.exec(type);
  if (curve) return `ECDSA P-${curve[1]}${sk}`;
  if (type === "ssh-rsa") return bits ? `RSA ${bits}` : "RSA";
  return type;
}

/** A source the job accepts. */
export function validSource(s: unknown): s is KeySource {
  if (s === "pasted" || s === "hetzner") return true;
  return typeof s === "string" && s.startsWith("github:") && GITHUB_USER.test(s.slice(7));
}

/** Where a key came from, for a sentence: "from GitHub (@sam)", "pasted", "from your Hetzner project". */
export function sourceWords(s: string): string {
  if (s.startsWith("github:")) return `from GitHub (@${s.slice(7)})`;
  if (s === "hetzner") return "from your Hetzner project";
  if (s === "dashboard") return "added in the dashboard";
  return "pasted";
}

export type KeyArg = { key: string; source: KeySource };

/**
 * The create request's sshKeys, checked for the setup job: each item one
 * accepted key and a known source, duplicates dropped, at most 10 and 16 KB.
 * Returns the keys as "type base64" (no comment travels). Throws KeyError.
 */
export function checkKeyArgs(v: unknown): KeyArg[] {
  if (v == null) return [];
  if (!Array.isArray(v)) throw new KeyError("bad SSH keys");
  if (v.length > MAX_KEYS) throw new KeyError(`at most ${MAX_KEYS} keys`);
  const out: KeyArg[] = [];
  const seen = new Set<string>();
  let size = 0;
  for (const item of v) {
    const key = (item as { key?: unknown })?.key;
    const source = (item as { source?: unknown })?.source;
    if (typeof key !== "string" || !validSource(source)) throw new KeyError("bad SSH keys");
    if (key.includes("\n")) throw new KeyError("one key per line");
    const k = parseLine(key);
    size += k.line.length + 1;
    if (seen.has(k.base64)) continue;
    seen.add(k.base64);
    out.push({ key: k.line, source });
  }
  if (size > MAX_BYTES) throw new KeyError(`that's more than ${MAX_BYTES / 1024} KB of keys`);
  return out;
}
