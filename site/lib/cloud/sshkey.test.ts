import { describe, expect, test } from "bun:test";
import { checkKeyArgs, fingerprint, keyKind, KeyError, parseKeys, parseLine, shortFingerprint, validSource } from "./sshkey";

// Real public keys, made with ssh-keygen for these tests (their private halves were thrown away).
const ED = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINbgit9Y8WPU/QBdrxDAtouyK+piPxWmFbJ3gKgMj4Po sam@laptop";
const ED_FP = "SHA256:7oJ928ukdFrY45owr81fVRyRDiHgQDndpc0H7aU96zY";
const ED2 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKKBxossFgmgXvLPqdBbqDw6arHj5RbNgjTx+iVvxCOz work";
const P256 =
  "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBFgPm3iex9Xtaycqsp3o7CAudIrP28PRMRrFZ4fCiWmqsSBgCV2llFGaKbarkjCMi3L+rlCZEUbNM3S/ShF6nFo= p256";
const P384 =
  "ecdsa-sha2-nistp384 AAAAE2VjZHNhLXNoYTItbmlzdHAzODQAAAAIbmlzdHAzODQAAABhBEJZxGYITARg4WPzM3vDXnKNm7z5jsst9vYFCXJpATFBexU+F9ErLl3gX+G3szv72m+PkG8OcA0I7+rzQinZusPVcQ40lG5VULW9ppU4nDauxicV7QyLPKW8+7WA96o9Uw==";
const P521 =
  "ecdsa-sha2-nistp521 AAAAE2VjZHNhLXNoYTItbmlzdHA1MjEAAAAIbmlzdHA1MjEAAACFBAC13vfeIoyNM1VSm/zmA6HpFDnWgcjf+mO62c9jpsEeb1MLtV2DhkYIZ/pidRcc1ItJAcGl1o83XwuyJHKOq61VmwESScZQojT60BaksRNXs4ttyTa2lUFgYV9MblOoLUquD9qgI/b5virAdjxCTpFi/ISfveHGa3R+bX4C8h3kNrL+qQ==";
const P521_FP = "SHA256:4LRLS0OMavLQ0YnzRU5Lj86qYK1Na8Vd07KaBZ5kxdA";
const RSA3072 =
  "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCyxMNeSK01W0Bc9LZnL7a8jUDc1Z4PQo+k8f5+G9TA1CFEN8Kxi0xvbg0LPPEPUSe8rWZCkVaHs2c2MlgO0RwZmn9+uF+O9xFKfXXLnt52nuThJYfQcJ/30hQUUQkDN/fKc+6zfgGZ5xGwrRSkh5JHcl5deNY1YkuFozASNkTNlxWoR3SLQiqkJuTUkCiWcix/cTqicojB5ZAbi/tjW0X4ixzVT48Ml5nkI6j8IAoDo2tTTBfJKoQ2QpYus//BNbX0Kx/A8MEFSFSfdPd+chEjd8L04v0Qe2VD5D9tk449CulJu3o/zqIFsKsxPYIUwuB6cpLvDwPmXsaiuG738gNypnBP2MKgW5E6aPX6vVmKrK9G7Mr9ZIX/s2ydVMzNFqc75jpReY2meQjEuHLTykRtV94ywWyNQOUpETsRLQmgRewtlddd7XSXGJfcI4Fxug58qBFpflYuqOZBsZkx1CQslAKZv1iOa44W+elkJRYMSCekScPn2LyGSPc8eyfxY/k= rsa3072";
const RSA3072_FP = "SHA256:owriZBxskWCEPn7e18TiklqOicu+Yrc9p+46leP/W98";
const RSA2048 =
  "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDfRmmyAazCDqmAjlND4cQNiksZCSyQb/QKTTLgpSE1xBeqdDj1ncitE00F7Sf96zdjnfEMhRnChd9Kb5cVPXwoc+SR5jq6ebeFVQV5mCNoscPjX9HghEs7t5yL/X+osSu+fKFwZHSuVIzRGo03AAUX7397RTsyGc57Od38YDad4GMtT0rJEbXPRSU4r6fzo/yP+kUOGCSfVzpuxCs9QxaggLM2QQmurVR6ECHih8ZWiHeesgoex/Q8OPQ5pn9V7Y+JdxoU8D+kLyK1NwWIX8K1LtxYnBYd6j/m5jPIAhd5IzeoZz6x/ALUDutngokyELPg5Tp1VK4aH6qJi2YBfS2P rsa2048";
const RSA4096 =
  "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQDe1CgsAX6WNAcTxCfqc2JUl6onColS+b/JK/tb2w5OT0tADCpYMCLghyOmVvzvsPwbe0uVAzvuV/eRE/v9+i3hu5clpmAdUIw4zuE0YmJPaFK7cdu/HBWkFChRb8bE8AlUotBM/90jUbmmKvhf806qo3dfNqjgq6oijoipVvhQk719lk7epoQa2clO+cPGcN8pPVNoAyPdjT3Mmy5T6bgKrWmzhB/Z0DndKJxUjMUnrnGj9slv3tusFpYUMB4Kx75FUfq4roNC8SCSn+amTxvSY/Yq+P5+j+HwOzrg8oiKSvTfdVH4v+Ij8q+7ePkzex8VmKpXnNHBNUkw5aDLHO3d1sQP2mIAEvt3ykmuFi0Lzeb375xpTyVOU4yJ1LZ4zFfR4KVljYv/2q79t/j/jhUgUDWj0GY23p/sGr/LUOOSfNy7CgHZDvqAh+Hi/5Fs9N6ZZh0NWuOKdRzIoSl/Hx+Jp4Q6Y9ZVKQtw3Ps/VQlvJWnSInFTDc2BOMNjawG4B6L653yyuR+BquKODhMhTKhb/JyxhVma8IkVf/7X6gALBYFETY9kWJ3w9O64ztvy2+SR/J9a/epzAjTgY0pgiwwMVx0V+6rj2AG/qYfUKlvq1P+zuk94W9SUnNJa0ba0BFdydNFuN+ClsHqzaLf5039V/m1FWeKDwK5aZd/5tREHxw==";
const RSA4096_FP = "SHA256:FwJsYzRPU99vUTD2bbFuobuy8b06NDyG+WOk8nCp+Gg";
const CERT =
  "ssh-ed25519-cert-v01@openssh.com AAAAIHNzaC1lZDI1NTE5LWNlcnQtdjAxQG9wZW5zc2guY29tAAAAIMGYxY8XorE5QbtrXq7yduN2gHJU9/1bYKx/8MBAuniIAAAAIKKBxossFgmgXvLPqdBbqDw6arHj5RbNgjTx+iVvxCOzAAAAAAAAAAAAAAABAAAABHRlc3QAAAAIAAAABHJvb3QAAAAAAAAAAP//////////AAAAAAAAAIIAAAAVcGVybWl0LVgxMS1mb3J3YXJkaW5nAAAAAAAAABdwZXJtaXQtYWdlbnQtZm9yd2FyZGluZwAAAAAAAAAWcGVybWl0LXBvcnQtZm9yd2FyZGluZwAAAAAAAAAKcGVybWl0LXB0eQAAAAAAAAAOcGVybWl0LXVzZXItcmMAAAAAAAAAAAAAADMAAAALc3NoLWVkMjU1MTkAAAAg1uCK31jxY9T9AF2vEMC2i7Ir6mI/FaYVsneAqAyPg+gAAABTAAAAC3NzaC1lZDI1NTE5AAAAQPYby+Yny7RpnCo9BU3F5XEujI9lb8JKmrGDp3OPfDlOUaIQPneBe+qTqDQ43+44qiRvgHqhZZTgj/i30O70Uwo= work";

/** A security-key public key in the wire format (ssh-keygen needs the hardware to make one). */
function wire(...parts: (string | Uint8Array)[]): string {
  const bytes: number[] = [];
  for (const p of parts) {
    const b = typeof p === "string" ? new TextEncoder().encode(p) : p;
    bytes.push((b.length >>> 24) & 255, (b.length >>> 16) & 255, (b.length >>> 8) & 255, b.length & 255, ...b);
  }
  return btoa(String.fromCharCode(...bytes));
}
const point = (n: number) => Uint8Array.from({ length: n }, (_, i) => (i === 0 ? 4 : i));
const SK_ED = `sk-ssh-ed25519@openssh.com ${wire("sk-ssh-ed25519@openssh.com", new Uint8Array(32).fill(7), "ssh:")} yubikey`;
const SK_ECDSA = `sk-ecdsa-sha2-nistp256@openssh.com ${wire("sk-ecdsa-sha2-nistp256@openssh.com", "nistp256", point(65), "ssh:")}`;

const reason = (f: () => unknown) => {
  try {
    f();
  } catch (e) {
    expect(e).toBeInstanceOf(KeyError);
    return (e as Error).message;
  }
  throw new Error("expected a KeyError");
};

describe("ssh public keys", () => {
  test("every accepted type, with the same fingerprint ssh-keygen prints", async () => {
    for (const k of [ED, ED2, P256, P384, P521, RSA3072, RSA4096, SK_ED, SK_ECDSA]) expect(parseLine(k).line).toBe(k.split(" ").slice(0, 2).join(" "));
    expect(await fingerprint(parseLine(ED))).toBe(ED_FP);
    expect(await fingerprint(parseLine(P521))).toBe(P521_FP);
    expect(await fingerprint(parseLine(RSA3072))).toBe(RSA3072_FP);
    expect(await fingerprint(parseLine(RSA4096))).toBe(RSA4096_FP);
    expect(parseLine(RSA3072).bits).toBe(3072);
    expect(parseLine(RSA4096).bits).toBe(4096);
    expect(parseLine(ED).comment).toBe("sam@laptop");
    expect(parseLine(`  ${ED}  `).type).toBe("ssh-ed25519");
  });

  test("short RSA, DSA, certificates, private keys and options are refused, with a reason", () => {
    expect(reason(() => parseLine(RSA2048))).toContain("at least 3072 bits (this one has 2048)");
    expect(reason(() => parseLine("ssh-dss AAAAB3NzaC1kc3MAAACBAP"))).toContain("ssh-dss keys aren't accepted");
    expect(reason(() => parseLine(CERT))).toContain("certificates");
    expect(reason(() => parseLine("-----BEGIN OPENSSH PRIVATE KEY-----"))).toContain("private key");
    expect(reason(() => parseKeys("-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"))).toContain("private key");
    expect(reason(() => parseLine(`command="rm -rf /" ${ED}`))).toContain("options");
    expect(reason(() => parseLine(`no-pty ${ED}`))).toContain("options");
    expect(reason(() => parseLine(`from="10.0.0.1",restrict ${ED}`))).toContain("options");
    expect(reason(() => parseLine("hello there"))).toBe("not an SSH public key");
  });

  test("two keys on one line, or a key that doesn't match its type, are refused", () => {
    expect(reason(() => parseLine(`${ED} ${ED2}`))).toBe("one key per line");
    // ed25519 data under an RSA label, a truncated key, bad base64, and trailing bytes.
    const edData = ED.split(" ")[1]!;
    expect(reason(() => parseLine(`ssh-rsa ${edData}`))).toContain("doesn't match");
    expect(reason(() => parseLine(`ssh-ed25519 ${edData.slice(0, 40)}`))).toBe("not an SSH public key");
    expect(reason(() => parseLine("ssh-ed25519 not*base64"))).toBe("not an SSH public key");
    expect(reason(() => parseLine(`ssh-ed25519 ${wire("ssh-ed25519", new Uint8Array(32), "extra")}`))).toBe("not an SSH public key");
    expect(reason(() => parseLine(`ssh-ed25519 ${wire("ssh-ed25519", new Uint8Array(31))}`))).toBe("not an SSH public key");
    // An ecdsa key whose curve isn't the one its type names.
    expect(reason(() => parseLine(`ecdsa-sha2-nistp384 ${wire("ecdsa-sha2-nistp384", "nistp256", point(65))}`))).toContain("curve");
    expect(reason(() => parseLine(`ecdsa-sha2-nistp256 ${wire("ecdsa-sha2-nistp256", "nistp256", point(64))}`))).toBe("not an SSH public key");
  });

  test("pasted text: one per line, blank lines and duplicates skipped, at most 10 and 16 KB", () => {
    const keys = parseKeys(`${ED}\r\n\n${P256}\n${ED.replace("sam@laptop", "again")}\n`);
    expect(keys.map((k) => k.type)).toEqual(["ssh-ed25519", "ecdsa-sha2-nistp256"]);
    expect(reason(() => parseKeys(`${ED}\n${RSA2048}`))).toStartWith("line 2: RSA keys need");
    expect(reason(() => parseKeys("  \n"))).toBe("no SSH public key found");
    const eleven = Array.from({ length: 11 }, (_, i) => `ssh-ed25519 ${wire("ssh-ed25519", new Uint8Array(32).fill(i))}`).join("\n");
    expect(reason(() => parseKeys(eleven))).toBe("at most 10 keys");
    expect(reason(() => parseKeys(ED + " ".repeat(17_000)))).toContain("16 KB");
  });

  test("the create request's keys: sources checked, comments dropped, duplicates once", () => {
    expect(checkKeyArgs(undefined)).toEqual([]);
    expect(checkKeyArgs([])).toEqual([]);
    const out = checkKeyArgs([
      { key: ED, source: "github:sam-k" },
      { key: P256, source: "pasted" },
      { key: ED, source: "hetzner" },
    ]);
    expect(out).toEqual([
      { key: ED.split(" ").slice(0, 2).join(" "), source: "github:sam-k" },
      { key: P256.split(" ").slice(0, 2).join(" "), source: "pasted" },
    ]);
    expect(reason(() => checkKeyArgs([{ key: ED, source: "github:-bad" }]))).toBe("bad SSH keys");
    expect(reason(() => checkKeyArgs([{ key: ED, source: "dashboard" }]))).toBe("bad SSH keys");
    expect(reason(() => checkKeyArgs([{ key: `${ED}\n${ED2}`, source: "pasted" }]))).toBe("one key per line");
    expect(reason(() => checkKeyArgs([{ key: RSA2048, source: "pasted" }]))).toContain("3072");
    expect(reason(() => checkKeyArgs("ssh-ed25519 AAAA"))).toBe("bad SSH keys");
    expect(reason(() => checkKeyArgs(Array.from({ length: 11 }, () => ({ key: ED, source: "pasted" }))))).toBe("at most 10 keys");
  });

  test("sources and words", () => {
    for (const ok of ["pasted", "hetzner", "github:sam", "github:a-b-c", "github:" + "a".repeat(39)]) expect(validSource(ok)).toBe(true);
    for (const bad of ["dashboard", "github:", "github:a--", "github:-a", "github:" + "a".repeat(40), "github:a b", "gitlab:sam", 3, null]) expect(validSource(bad)).toBe(false);
    expect(keyKind("ssh-ed25519")).toBe("ED25519");
    expect(keyKind("sk-ssh-ed25519@openssh.com")).toBe("ED25519 security key");
    expect(keyKind("ecdsa-sha2-nistp384")).toBe("ECDSA P-384");
    expect(keyKind("ssh-rsa", 4096)).toBe("RSA 4096");
    expect(shortFingerprint(ED_FP)).toBe("SHA256:7oJ928uk…aU96zY");
  });
});
