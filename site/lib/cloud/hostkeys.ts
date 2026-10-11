// The server's identity as the setup worker saw it (cloud_boxes.host_keys:
// known_hosts lines for <name>.shiptiffin.app and the IPv4), for the account
// page: the lines to copy, and each key's fingerprint to compare with what
// ssh shows the first time. Server side only.
import { createHash } from "node:crypto";
import { boxDomain } from "./names";

const LINE = /^(\S+) (ssh-ed25519|ecdsa-sha2-nistp(?:256|384|521)|ssh-rsa) ([A-Za-z0-9+/]+={0,2})$/;

/** The recorded lines that are well-formed known_hosts lines, once each. */
export function knownHosts(text: string | null): string[] {
  const out = new Set<string>();
  for (const raw of (text ?? "").split("\n")) {
    const l = raw.trim().replace(/\s+/g, " ");
    if (LINE.test(l)) out.add(l);
  }
  return [...out];
}

/** Each host key once, with ssh's SHA256 fingerprint: { kind: "ED25519", fingerprint: "SHA256:…" }. */
export function hostFingerprints(lines: string[]): { kind: string; fingerprint: string }[] {
  const seen = new Map<string, { kind: string; fingerprint: string }>();
  for (const l of lines) {
    const m = LINE.exec(l);
    if (!m || seen.has(m[3]!)) continue;
    const kind = m[2] === "ssh-ed25519" ? "ED25519" : m[2] === "ssh-rsa" ? "RSA" : `ECDSA P-${m[2]!.slice(-3)}`;
    const fingerprint = "SHA256:" + createHash("sha256").update(Buffer.from(m[3]!, "base64")).digest("base64").replace(/=+$/, "");
    seen.set(m[3]!, { kind, fingerprint });
  }
  return [...seen.values()];
}

/** The one line that gets an owner with an SSH key back into their dashboard. */
export const recoveryCommand = (name: string) => `ssh root@${boxDomain(name)} tiffin login`;
