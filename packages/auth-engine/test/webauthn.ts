// A software passkey for tests: makes the registration and authentication
// responses a browser's authenticator would, with a P-256 key ("none"
// attestation), for a given rpID and origin.

const enc = new TextEncoder();
const b64u = (b: Uint8Array) => Buffer.from(b).toString("base64url");
const sha256 = async (b: Uint8Array) => new Uint8Array(await crypto.subtle.digest("SHA-256", b as BufferSource));
const cat = (...parts: Uint8Array[]) => {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let i = 0;
  for (const p of parts) out.set(p, (i += p.length) - p.length);
  return out;
};

// Just enough CBOR: unsigned/negative ints, byte and text strings, maps.
function cbor(v: unknown): Uint8Array {
  const head = (major: number, n: number) =>
    n < 24 ? Uint8Array.of((major << 5) | n) : n < 256 ? Uint8Array.of((major << 5) | 24, n) : Uint8Array.of((major << 5) | 25, n >> 8, n & 255);
  if (typeof v === "number") return v >= 0 ? head(0, v) : head(1, -1 - v);
  if (typeof v === "string") return cat(head(3, enc.encode(v).length), enc.encode(v));
  if (v instanceof Uint8Array) return cat(head(2, v.length), v);
  if (v instanceof Map) return cat(head(5, v.size), ...[...v].flatMap(([k, x]) => [cbor(k), cbor(x)]));
  throw new Error("cbor: unsupported value");
}

// WebCrypto signs ECDSA as r||s; WebAuthn wants DER.
function der(sig: Uint8Array): Uint8Array {
  const int = (b: Uint8Array) => {
    let i = 0;
    while (i < b.length - 1 && b[i] === 0) i++;
    let v = b.slice(i);
    if (v[0]! & 0x80) v = cat(Uint8Array.of(0), v);
    return cat(Uint8Array.of(2, v.length), v);
  };
  const body = cat(int(sig.slice(0, 32)), int(sig.slice(32)));
  return cat(Uint8Array.of(0x30, body.length), body);
}

export class SoftPasskey {
  readonly id = crypto.getRandomValues(new Uint8Array(16));
  private keys!: CryptoKeyPair;
  private count = 0;

  static async create() {
    const p = new SoftPasskey();
    p.keys = (await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"])) as CryptoKeyPair;
    return p;
  }

  private clientData(type: string, challenge: string, origin: string) {
    return enc.encode(JSON.stringify({ type, challenge, origin, crossOrigin: false }));
  }

  /** The body for POST /passkey/verify-registration. */
  async register(options: { challenge: string; rp: { id: string } }, origin: string) {
    const jwk = await crypto.subtle.exportKey("jwk", this.keys.publicKey);
    const coord = (s: string) => new Uint8Array(Buffer.from(s, "base64url"));
    const cose = cbor(new Map<number, unknown>([[1, 2], [3, -7], [-1, 1], [-2, coord(jwk.x!)], [-3, coord(jwk.y!)]]));
    const authData = cat(await sha256(enc.encode(options.rp.id)), Uint8Array.of(0x45), new Uint8Array(4), new Uint8Array(16), Uint8Array.of(0, this.id.length), this.id, cose);
    const attestationObject = cbor(new Map<string, unknown>([["fmt", "none"], ["attStmt", new Map()], ["authData", authData]]));
    return {
      response: {
        id: b64u(this.id),
        rawId: b64u(this.id),
        type: "public-key",
        clientExtensionResults: {},
        response: { clientDataJSON: b64u(this.clientData("webauthn.create", options.challenge, origin)), attestationObject: b64u(attestationObject), transports: ["internal"] },
      },
    };
  }

  /** The body for POST /passkey/verify-authentication. */
  async authenticate(options: { challenge: string }, rpID: string, origin: string) {
    const authData = cat(await sha256(enc.encode(rpID)), Uint8Array.of(0x05), Uint8Array.of(0, 0, 0, ++this.count));
    const clientDataJSON = this.clientData("webauthn.get", options.challenge, origin);
    const sig = new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, this.keys.privateKey, cat(authData, await sha256(clientDataJSON))));
    return {
      response: {
        id: b64u(this.id),
        rawId: b64u(this.id),
        type: "public-key",
        clientExtensionResults: {},
        response: { clientDataJSON: b64u(clientDataJSON), authenticatorData: b64u(authData), signature: b64u(der(sig)) },
      },
    };
  }
}
