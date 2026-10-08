import { describe, expect, test } from "bun:test";
import { createDecipheriv, createPrivateKey, diffieHellman, sign, createPublicKey } from "node:crypto";
import { licencePublicFrom, verifyLicence } from "./licence";
import { fingerprint, rawX25519, seal, sealKey, sealPublicFrom, tokenAAD } from "./seal";

// The same vectors as internal/cloud/seal_test.go and internal/licence/licence_test.go.
// The worker's test key: X25519 private key bytes 0..31.
const WORKER_PRIV = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=";
const WORKER_PUB = "j0DFrbaPJWJK5bIU6nZ6bslNgp09e14a0bpvPiE4KF8=";
const GO_SEALED = "v2.IdYqSZX2ew94r60lDK2K5k8eXQZaS71tPbwE2SzTQmg._qXNSMZSv9JuZ_qxLTJruFYuSVwdwyIwh5G4tu54vfsqdFWnwpmgYqjGxkc";
// The licence seed's public half (seed bytes 0..31).
const LICENCE_PUB = "A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg=";
const GO_LICENCE =
  "tl1.eyJ2IjoxLCJib3giOiJib3hfdGVzdDEiLCJuYW1lIjoic2hvcCIsImRvbWFpbiI6InNob3Auc2hpcHRpZmZpbi5hcHAiLCJpYXQiOjE3OTE0MjQwMDAsImdlbiI6M30.d1O3mppmVMiRh37mPtUUV_-4G8yRYmQTvrGbryTHHmNCLDrVPg2Ezrf8sXpr9Po0VMIYm9UiPbMcteiBtKh8DA";

// The website never opens a sealed value; this test-only opener checks the
// format against the Go side (the worker's real opener is internal/cloud/seal.go).
function openForTest(privB64: string, sealed: string, aad: string): string | null {
  const m = /^v2\.([A-Za-z0-9_-]+)\.([A-Za-z0-9_-]+)$/.exec(sealed);
  if (!m) return null;
  const raw = Buffer.from(privB64, "base64");
  const priv = createPrivateKey({ key: { kty: "OKP", crv: "X25519", d: raw.toString("base64url"), x: "" } as any, format: "jwk" });
  const ephPub = Buffer.from(m[1]!, "base64url");
  const eph = createPublicKey({ key: { kty: "OKP", crv: "X25519", x: ephPub.toString("base64url") }, format: "jwk" });
  const key = sealKey(diffieHellman({ privateKey: priv, publicKey: eph }), ephPub, rawX25519(createPublicKey(priv)));
  const body = Buffer.from(m[2]!, "base64url");
  try {
    const d = createDecipheriv("aes-256-gcm", key, body.subarray(0, 12));
    d.setAAD(Buffer.from(aad));
    d.setAuthTag(body.subarray(body.length - 16));
    return Buffer.concat([d.update(body.subarray(12, body.length - 16)), d.final()]).toString("utf8");
  } catch {
    return null;
  }
}

describe("seal", () => {
  const pub = sealPublicFrom(WORKER_PUB)!;

  test("seals to the worker's public key, bound to its row", () => {
    const s = seal(pub, "hcloud-token-123", tokenAAD("box_a"));
    expect(s.startsWith("v2.")).toBe(true);
    expect(s).not.toContain("hcloud-token-123");
    expect(openForTest(WORKER_PRIV, s, tokenAAD("box_a"))).toBe("hcloud-token-123");
    expect(openForTest(WORKER_PRIV, s, tokenAAD("box_b"))).toBeNull();
    expect(openForTest(Buffer.alloc(32, 7).toString("base64"), s, tokenAAD("box_a"))).toBeNull();
    expect(openForTest(WORKER_PRIV, s.slice(0, -4) + "AAAA", tokenAAD("box_a"))).toBeNull();
    expect(seal(pub, "x", "a")).not.toBe(seal(pub, "x", "a"));
  });

  test("the derivation matches the Go worker's", () => {
    expect(openForTest(WORKER_PRIV, GO_SEALED, tokenAAD("box_vector"))).toBe("hcloud-go-vector");
  });

  test("public key must be 32 bytes", () => {
    expect(sealPublicFrom(undefined)).toBeNull();
    expect(sealPublicFrom("c2hvcnQ=")).toBeNull();
    expect(sealPublicFrom(WORKER_PUB)).not.toBeNull();
  });

  test("fingerprint is short and stable", () => {
    expect(fingerprint(" abc\n")).toBe(fingerprint("abc"));
    expect(fingerprint("abc")).toHaveLength(12);
  });

  // Prints a vector for internal/cloud/seal_test.go (tsSealed): the worker opens it.
  test("vector for Go", () => {
    const s = seal(pub, "hcloud-vector-token", tokenAAD("box_vector"));
    if (process.env.PRINT_VECTOR) console.log("TSSEALED " + s);
    expect(openForTest(WORKER_PRIV, s, tokenAAD("box_vector"))).toBe("hcloud-vector-token");
  });
});

describe("licence", () => {
  const pub = licencePublicFrom(LICENCE_PUB)!;

  test("verifies the Go-signed vector with the public key alone", () => {
    expect(verifyLicence(pub, GO_LICENCE)).toEqual({ v: 1, box: "box_test1", name: "shop", domain: "shop.shiptiffin.app", iat: 1791424000, gen: 3 });
  });

  test("refuses changed, foreign and malformed tokens", () => {
    const [p, body, sig] = GO_LICENCE.split(".");
    const swapped = `${p}.${Buffer.from(JSON.stringify({ v: 1, box: "box_other", name: "x" })).toString("base64url")}.${sig}`;
    const other = createPrivateKey({ key: Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), Buffer.alloc(32, 9)]), format: "der", type: "pkcs8" });
    const forgedBody = `tl1.${body}`;
    const forged = `${forgedBody}.${sign(null, Buffer.from(forgedBody), other).toString("base64url")}`;
    for (const t of ["", "tl1.", swapped, forged, GO_LICENCE.slice(0, -3), `${p}.${body}`, GO_LICENCE.replace("tl1.", "tl2.")]) {
      expect(verifyLicence(pub, t)).toBeNull();
    }
    expect(licencePublicFrom("c2hvcnQ=")).toBeNull();
  });
});
