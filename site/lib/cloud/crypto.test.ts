import { describe, expect, test } from "bun:test";
import { createPrivateKey, sign } from "node:crypto";
import { publicKeyFromSeed, verifyLicence } from "./licence";
import { fingerprint, kekFrom, open, ownerAAD, seal, tokenAAD } from "./seal";

// The same vectors as internal/cloud/seal_test.go and internal/licence/licence_test.go.
const KEK = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=";
const GO_SEALED =
  "v1.b39NKLEi4QBWggGeqppbcynpdUfBeoz4-cJnwBCKbtWEEkE_gWbPG7jCp2onEuFgxjG8hm_vE5OecKlo.evbBQIV0U4M6J1vf7VLIj_fF3kBqnqBfKu0xczRhoMGgYt4UiJuShHIKJ4TgNDM";
const SEED = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=";
const GO_LICENCE =
  "tl1.eyJ2IjoxLCJib3giOiJib3hfdGVzdDEiLCJuYW1lIjoic2hvcCIsImRvbWFpbiI6InNob3Auc2hpcHRpZmZpbi5hcHAiLCJpYXQiOjE3OTE0MjQwMDAsImdlbiI6M30.d1O3mppmVMiRh37mPtUUV_-4G8yRYmQTvrGbryTHHmNCLDrVPg2Ezrf8sXpr9Po0VMIYm9UiPbMcteiBtKh8DA";

describe("seal", () => {
  const kek = kekFrom(KEK)!;

  test("round trip, bound to its row", () => {
    const s = seal(kek, "hcloud-token-123", tokenAAD("box_a"));
    expect(s.startsWith("v1.")).toBe(true);
    expect(s).not.toContain("hcloud-token-123");
    expect(open(kek, s, tokenAAD("box_a"))).toBe("hcloud-token-123");
    expect(open(kek, s, tokenAAD("box_b"))).toBeNull();
    expect(open(kek, s, ownerAAD("box_a"))).toBeNull();
    expect(open(Buffer.alloc(32, 7), s, tokenAAD("box_a"))).toBeNull();
    expect(open(kek, s.slice(0, -4) + "AAAA", tokenAAD("box_a"))).toBeNull();
    expect(open(kek, "nope", tokenAAD("box_a"))).toBeNull();
    expect(seal(kek, "x", "a")).not.toBe(seal(kek, "x", "a"));
  });

  test("opens what the Go worker sealed", () => {
    expect(open(kek, GO_SEALED, tokenAAD("box_vector"))).toBe("hcloud-vector-token");
  });

  test("KEK must be 32 bytes", () => {
    expect(kekFrom(undefined)).toBeNull();
    expect(kekFrom("c2hvcnQ=")).toBeNull();
    expect(kekFrom(KEK)?.length).toBe(32);
  });

  test("fingerprint is short and stable", () => {
    expect(fingerprint(" abc\n")).toBe(fingerprint("abc"));
    expect(fingerprint("abc")).toHaveLength(12);
  });

  // Prints a vector for internal/cloud/seal_test.go (sealedVector).
  test("vector for Go", () => {
    const s = seal(kek, "hcloud-vector-token", tokenAAD("box_vector"));
    if (process.env.PRINT_VECTOR) console.log("TSSEALED " + s);
    expect(open(kek, s, tokenAAD("box_vector"))).toBe("hcloud-vector-token");
  });
});

describe("licence", () => {
  const pub = publicKeyFromSeed(SEED)!;

  test("verifies the Go-signed vector", () => {
    const l = verifyLicence(pub, GO_LICENCE);
    expect(l).toEqual({ v: 1, box: "box_test1", name: "shop", domain: "shop.shiptiffin.app", iat: 1791424000, gen: 3 });
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
    expect(publicKeyFromSeed("c2hvcnQ=")).toBeNull();
  });
});
