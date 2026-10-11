import { expect, test } from "bun:test";
import type { Recovery } from "@/api/access";
import { afterLinkSignIn, noWayBack } from "@/lib/recovery";
import {
  addedWords,
  fingerprint,
  githubLogin,
  keysSentence,
  mostRecent,
  parseKeyLine,
  parseKeys,
  shortFingerprint,
  sourceWords,
  typeWords,
  type PublicKey,
} from "@/lib/ssh-key";

// Made with ssh-keygen for this test; fingerprints from ssh-keygen -lf.
const ED = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHKq0v0yFh/47lbj5sQMUpPkOwLWUc/b3fd1Mdnzy590 sam@laptop";
const RSA3072 =
  "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDDOuRBGRQ/n/bK+NCjuPdjP26BAMcOORmNmepOnfZmLTjtn1hG1PA8wxzMYa3AaCu18Ejy4j9tau7SuQi0v3lAAOiE0mwICpIa813AgvUCjq1lDs73Tgb6n3pPbRPv3iRigs+qKsxWiQEqiKERhEPjIeis9SbVvO7bWqQ3gwuGoAZ6EcOxMunAO4QS95mjyIBcVHZ3tOai88RO6coeb5pwHXoRcwIry5fs1W+nz5mHkZ8j1l5LleQSonA5XwsoZXU5h3Q8cPMggUQKdh598Z7yv7iVjwG0A6lbWW8hst2nbb7bhvm7stAgL1ndxhA3GwoyCnmQ2mpEDN+N7p32PP1bY1p0YDZdYtOHAKsULYC+RzLgiIGUOR9q8+by4GmcZH6WX93UPHGIQU/xdpjZHPB6TuWBj8EutPLS+rZQrLdMpC/xLZo8nGbluvt+RdF9wse8VtBukP+KGAYzLeT2D3QhcUULLYb9vp4cuycQsFOmLrA5bOK53lpDN8qJ8d3jD8s= r3";
const RSA2048 =
  "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDUWMm32uRCKqlbSp6xhVzgyNdJJ1Sp7i+okMQyZmgmRTOK30NYJo+YXVVB6rf+FIp8ALGX3WZoS1QFDrfRqjOao6DXpODtPOf7vvt8nn0HJkzSCqOKdq0XWIdD2H6OSBWcAjqp0SgTVqgPwBEqw66+DWJ7NXYl7Gkx7v1EyNzYPHLL8TnLkwp7iWovz2PZ0Vez5MEsSjCODixnaIVe+1fDrodvLTVTAbewvUAQXpCn+kkrb9wp7DVjazRCRb8xF6+Kss55k6FH+dOIhVhFA1wfOvjnbYUKCryAZW9MI9gB3k3ZIRKyQyZHYfG6zVcrQQ3gy1QoEsboU7YnVxR3xQX9 r2";
const EC =
  "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBB/eWNtLWuTulfibhyaJe1b5D0lyVWqnwxck7M8WAcGAtOYlrXw7lvFMpgRL5c7LsShY8hkz2Nuimi+WlWqFLa8= ec";

const key = (line: string) => parseKeyLine(line) as PublicKey;

/** A well-formed Ed25519 key line whose 32 key bytes are all n. */
function edLine(n: number) {
  const type = new TextEncoder().encode("ssh-ed25519");
  const b = new Uint8Array([0, 0, 0, type.length, ...type, 0, 0, 0, 32, ...new Uint8Array(32).fill(n)]);
  return `ssh-ed25519 ${btoa(String.fromCharCode(...b))}`;
}

test("a public key parses, with the same fingerprint ssh-keygen prints", async () => {
  const k = key(ED);
  expect(k.type).toBe("ssh-ed25519");
  expect(k.comment).toBe("sam@laptop");
  expect(k.line).toBe(ED.replace(" sam@laptop", ""));
  expect(await fingerprint(k.blob)).toBe("SHA256:9cK6Za/hVGxF+G5ktjOexzlMd7851MbW/xntSkaP+gM");
  expect(await fingerprint(key(RSA3072).blob)).toBe("SHA256:0DRAmRR8kMubDTwSSaFqTJom1Seq9Y02blEfziVtT08");
  expect(await fingerprint(key(EC).blob)).toBe("SHA256:yCq8jZoMtNHJ/RR777mN6LbVnovHPBqRMs8TC0ModcU");
});

test("RSA keys say their size, and under 3072 bits are refused", () => {
  expect(key(RSA3072).bits).toBe(3072);
  expect(parseKeyLine(RSA2048)).toContain("2048-bit");
});

test("what isn't a usable public key says why", () => {
  expect(parseKeyLine("-----BEGIN OPENSSH PRIVATE KEY-----")).toContain("private key");
  expect(parseKeyLine("ssh-dss AAAAB3NzaC1kc3M=")).toContain("DSA");
  expect(parseKeyLine("ssh-ed25519-cert-v01@openssh.com AAAA")).toContain("Certificates");
  expect(parseKeyLine("hello there")).toContain("doesn’t look like a public SSH key");
  expect(parseKeyLine("ssh-ed25519 AAAAnotbase64!!")).toContain("cut short");
  // Valid base64, but the key inside names another type.
  expect(parseKeyLine(`ssh-rsa ${ED.split(" ")[1]}`)).toContain("cut short");
  // Cut off halfway.
  expect(parseKeyLine(RSA3072.slice(0, 200))).toContain("cut short");
});

test("a paste: several lines, blanks and comments skipped, duplicates once, the bad line named", () => {
  expect(parseKeys(`\n# my keys\n${ED}\n\n${EC}\n${ED}`).keys.map((k) => k.type)).toEqual(["ssh-ed25519", "ecdsa-sha2-nistp256"]);
  expect(parseKeys(`${ED}\n${RSA2048}`).problem).toStartWith("Line 2: ");
  expect(parseKeys(RSA2048).problem).not.toStartWith("Line");
  expect(parseKeys(Array.from({ length: 11 }, (_, i) => edLine(i)).join("\n")).problem).toBe("Up to 10 keys at a time.");
  expect(parseKeys(Array.from({ length: 10 }, (_, i) => edLine(i)).join("\n")).keys).toHaveLength(10);
});

test("keys in words", () => {
  expect(typeWords("ssh-ed25519")).toBe("Ed25519");
  expect(typeWords("sk-ssh-ed25519@openssh.com")).toBe("Ed25519 security key");
  expect(typeWords("ecdsa-sha2-nistp384")).toBe("ECDSA");
  expect(typeWords("ssh-rsa", 4096)).toBe("RSA 4096");
  expect(sourceWords("github:sam")).toBe("GitHub @sam");
  expect(sourceWords("pasted")).toBe("Pasted");
  expect(sourceWords("hetzner")).toBe("Hetzner");
  expect(shortFingerprint("SHA256:9cK6Za/hVGxF+G5ktjOexzlMd7851MbW/xntSkaP+gM")).toBe("SHA256:9cK6Za/h…P+gM");
  expect(keysSentence(0)).toBe("No SSH keys yet.");
  expect(keysSentence(1)).toBe("One key can log in as root.");
  expect(keysSentence(2)).toBe("Two keys can log in as root.");
});

test("when a key was added, as a day", () => {
  const now = new Date(2026, 9, 11, 9, 30);
  expect(addedWords("2026-10-11", now)).toBe("today");
  expect(addedWords("2026-10-10", now)).toBe("yesterday");
  expect(addedWords("2026-10-03", now)).toBe("on 3 Oct");
  expect(addedWords("2025-12-24", now)).toBe("on 24 Dec 2025");
  expect(addedWords("nonsense", now)).toBe("");
});

test("GitHub usernames as people type them", () => {
  expect(githubLogin("sam")).toBe("sam");
  expect(githubLogin(" @sam-doe ")).toBe("sam-doe");
  expect(githubLogin("https://github.com/sam.keys")).toBe("sam");
  expect(githubLogin("-sam")).toBeNull();
  expect(githubLogin("sam--doe")).toBeNull();
  expect(githubLogin("sam/../x")).toBeNull();
  expect(githubLogin("a".repeat(40))).toBeNull();
  expect(githubLogin("")).toBeNull();
});

test("the GitHub key ticked first is the one used last, else the newest", () => {
  expect(mostRecent([])).toBeUndefined();
  expect(
    mostRecent([
      { id: 1, key: "", created_at: "2024-01-01T00:00:00Z", last_used: "2026-01-01T00:00:00Z" },
      { id: 2, key: "", created_at: "2026-05-01T00:00:00Z", last_used: null },
      { id: 3, key: "", created_at: "2023-01-01T00:00:00Z", last_used: "2026-09-01T00:00:00Z" },
    ]),
  ).toBe(3);
  expect(
    mostRecent([
      { id: 1, key: "", created_at: "2024-01-01T00:00:00Z" },
      { id: 2, key: "", created_at: "2026-05-01T00:00:00Z" },
    ]),
  ).toBe(2);
  // GitHub left the dates out: the highest id (the newest).
  expect(mostRecent([{ id: 5, key: "" }, { id: 9, key: "" }])).toBe(9);
});

const none: Recovery = { passkeys: 0, sshKeys: 0, email: false, providers: 0, firstPasskeyOpen: true };

test("no way back in only when nothing at all could sign this person in again", () => {
  expect(noWayBack(undefined)).toBe(false);
  expect(noWayBack(none)).toBe(true);
  expect(noWayBack({ ...none, passkeys: 1 })).toBe(false);
  expect(noWayBack({ ...none, sshKeys: 1 })).toBe(false);
  expect(noWayBack({ ...none, email: true })).toBe(false);
  expect(noWayBack({ ...none, providers: 2 })).toBe(false);
});

test("after a link sign-in, someone with no passkey gets the step that offers one, then goes on", () => {
  expect(afterLinkSignIn(none, true, undefined)).toBe("/welcome/passkey");
  expect(afterLinkSignIn(none, true, "/projects/shop")).toBe("/welcome/passkey?next=%2Fprojects%2Fshop");
  expect(afterLinkSignIn({ ...none, passkeys: 1 }, true, "/projects/shop")).toBe("/projects/shop");
  // A browser that can't make one, or a box that didn't say: straight on.
  expect(afterLinkSignIn(none, false, undefined)).toBe("/");
  expect(afterLinkSignIn(undefined, true, "/usage")).toBe("/usage");
});
