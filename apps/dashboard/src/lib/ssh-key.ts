import { countWords } from "./format";

// Public SSH keys in the browser: enough parsing for quick feedback and to
// show the same SHA256 fingerprint `ssh-keygen -lf` prints. The box parses
// every key again and is the one that decides.

const TYPES = [
  "ssh-ed25519",
  "sk-ssh-ed25519@openssh.com",
  "ecdsa-sha2-nistp256",
  "ecdsa-sha2-nistp384",
  "ecdsa-sha2-nistp521",
  "sk-ecdsa-sha2-nistp256@openssh.com",
  "ssh-rsa",
];

export type PublicKey = { type: string; blob: Uint8Array; bits?: number; comment: string; line: string };

/** One authorized_keys line, or why it isn't a key the box takes. */
export function parseKeyLine(line: string): PublicKey | string {
  const t = line.trim();
  if (/^-----BEGIN/.test(t) || /PRIVATE KEY/.test(t)) return "That’s a private key. Paste the public one (the .pub file); never share the private one.";
  const [type, b64, ...rest] = t.split(/\s+/);
  if (!TYPES.includes(type)) {
    if (/^ssh-dss$/.test(type)) return "DSA keys aren’t accepted. Make an Ed25519 key instead.";
    if (/-cert-v01@openssh\.com$/.test(type)) return "Certificates aren’t accepted. Paste the plain public key.";
    return "That doesn’t look like a public SSH key. It starts with ssh-ed25519, ecdsa-sha2-… or ssh-rsa.";
  }
  let blob: Uint8Array;
  try {
    blob = Uint8Array.from(atob(b64 ?? ""), (c) => c.charCodeAt(0));
  } catch {
    return "The key is cut short or has extra characters in it. Copy the whole line again.";
  }
  const r = reader(blob);
  if (blob.length === 0 || r.text() !== type) return "The key is cut short or has extra characters in it. Copy the whole line again.";
  let bits: number | undefined;
  if (type === "ssh-rsa") {
    try {
      r.bytes(); // e
      bits = mpintBits(r.bytes());
    } catch {
      return "The key is cut short or has extra characters in it. Copy the whole line again.";
    }
    if (bits < 3072) return `This RSA key is ${bits}-bit; the server needs 3072 or more. Make an Ed25519 key instead.`;
  }
  return { type, blob, bits, comment: rest.join(" "), line: `${type} ${b64}` };
}

/** Every non-blank line of a paste: the keys, and the first problem (with its line number when there are several). */
export function parseKeys(text: string): { keys: PublicKey[]; problem?: string } {
  const lines = text.split(/\r?\n/).map((l) => l.trim()).filter((l) => l && !l.startsWith("#"));
  const keys: PublicKey[] = [];
  for (const [i, l] of lines.entries()) {
    const k = parseKeyLine(l);
    if (typeof k === "string") return { keys, problem: lines.length > 1 ? `Line ${i + 1}: ${k}` : k };
    if (!keys.some((x) => x.line === k.line)) keys.push(k);
  }
  if (keys.length > 10) return { keys, problem: "Up to 10 keys at a time." };
  return { keys };
}

function reader(b: Uint8Array) {
  let at = 0;
  const bytes = () => {
    if (at + 4 > b.length) throw new RangeError("short");
    const n = ((b[at] << 24) | (b[at + 1] << 16) | (b[at + 2] << 8) | b[at + 3]) >>> 0;
    at += 4;
    if (at + n > b.length) throw new RangeError("short");
    const out = b.subarray(at, at + n);
    at += n;
    return out;
  };
  return {
    bytes,
    text: () => {
      try {
        return new TextDecoder().decode(bytes());
      } catch {
        return "";
      }
    },
  };
}

function mpintBits(n: Uint8Array): number {
  let i = 0;
  while (i < n.length && n[i] === 0) i++;
  if (i === n.length) return 0;
  return (n.length - i - 1) * 8 + (32 - Math.clz32(n[i]));
}

/** "SHA256:…", as ssh-keygen -lf prints it. */
export async function fingerprint(blob: Uint8Array): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", blob as BufferSource));
  return `SHA256:${btoa(String.fromCharCode(...d)).replace(/=+$/, "")}`;
}

/** "SHA256:uNiVztks…Cs4w": enough to tell keys apart at a glance; the whole one is in the title. */
export function shortFingerprint(fp: string): string {
  const h = fp.replace(/^SHA256:/, "");
  return h.length > 14 ? `SHA256:${h.slice(0, 8)}…${h.slice(-4)}` : fp;
}

/** "Ed25519", "RSA 4096", "Ed25519 security key". */
export function typeWords(type: string, bits?: number): string {
  const sk = type.startsWith("sk-") ? " security key" : "";
  if (type.includes("ed25519")) return `Ed25519${sk}`;
  if (type.includes("ecdsa")) return `ECDSA${sk}`;
  if (type === "ssh-rsa") return bits ? `RSA ${bits}` : "RSA";
  return type;
}

/** Where a key came from: "GitHub @sam", "Pasted", "Hetzner". */
export function sourceWords(source: string): string {
  if (source.startsWith("github:")) return `GitHub @${source.slice(7)}`;
  return { pasted: "Pasted", hetzner: "Hetzner", dashboard: "Added here" }[source] ?? source;
}

/** "today", "yesterday", "on 3 Oct", "on 3 Oct 2025", from a date or a timestamp. */
export function addedWords(added: string, now = new Date()): string {
  const d = new Date(/^\d{4}-\d{2}-\d{2}$/.test(added) ? `${added}T12:00:00` : added);
  if (Number.isNaN(d.getTime())) return "";
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(now) - day(d)) / 86_400_000);
  if (diff === 0) return "today";
  if (diff === 1) return "yesterday";
  const opts: Intl.DateTimeFormatOptions = { day: "numeric", month: "short", ...(d.getFullYear() !== now.getFullYear() ? { year: "numeric" } : {}) };
  return `on ${d.toLocaleDateString("en-GB", opts)}`;
}

/** The page's state in one sentence. */
export function keysSentence(n: number): string {
  return n === 0 ? "No SSH keys yet." : `${countWords(n, "key", "keys", true)} can log in as root.`;
}

// ---------------------------------------------------------------- GitHub

/** A GitHub username as typed ("@sam", " sam "), or null when it can't be one. */
export function githubLogin(s: string): string | null {
  const u = s.trim().replace(/^@/, "").replace(/^https?:\/\/github\.com\//i, "").replace(/(\.keys)?\/?$/, "");
  return /^[A-Za-z0-9](?:[A-Za-z0-9]|-(?=[A-Za-z0-9])){0,38}$/.test(u) ? u : null;
}

/** GitHub's public key list (GET /users/<u>/keys): created_at and last_used are optional. */
export type GitHubKey = { id: number; key: string; created_at?: string; last_used?: string | null };
export type GitHubUser = { login: string; name?: string | null; avatar_url?: string };

/** The key to tick first: the one used most recently, else the newest. */
export function mostRecent(keys: GitHubKey[]): number | undefined {
  const at = (k: GitHubKey) => k.last_used ?? "";
  const made = (k: GitHubKey) => k.created_at ?? "";
  return [...keys].sort((a, b) => at(b).localeCompare(at(a)) || made(b).localeCompare(made(a)) || b.id - a.id)[0]?.id;
}

/**
 * The browser asks GitHub directly (its public API allows it, without
 * signing in, 60 times an hour per address); the box never calls GitHub
 * for this. Throws an Error with words to show.
 */
export async function fetchGitHubKeys(login: string, signal?: AbortSignal): Promise<{ user: GitHubUser; keys: GitHubKey[] }> {
  const get = async <T>(path: string): Promise<T> => {
    let res: Response;
    try {
      res = await fetch(`https://api.github.com${path}`, { headers: { Accept: "application/vnd.github+json" }, signal });
    } catch (e) {
      if (signal?.aborted) throw e;
      throw new Error("Couldn’t reach GitHub from this browser. Paste the key instead.", { cause: e });
    }
    if (res.status === 404) throw new Error(`There’s no GitHub account called ${login}.`);
    if (res.status === 403 || res.status === 429) throw new Error("GitHub is limiting lookups from this network for now. Try again in a while, or paste the key.");
    if (!res.ok) throw new Error("GitHub didn’t answer. Try again, or paste the key.");
    return res.json() as Promise<T>;
  };
  const [user, keys] = await Promise.all([get<GitHubUser>(`/users/${encodeURIComponent(login)}`), get<GitHubKey[]>(`/users/${encodeURIComponent(login)}/keys`)]);
  return { user, keys };
}
