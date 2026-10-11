// "From GitHub" on /start: the owner's browser asks api.github.com for a
// profile and its public SSH keys (both answer any origin, without a token,
// 60 times an hour per network). Our server never contacts GitHub; what the
// owner ticks goes with Create as it was shown. Browser-safe.
import { GITHUB_USER } from "./sshkey";

export type GitHubProfile = { login: string; name: string | null; avatar: string };
/** A key as GitHub lists it. created_at and last_used are null or missing on some keys. */
export type GitHubKey = { id: number; key: string; created_at?: string | null; last_used?: string | null };

export type GitHubAnswer = { ok: true; profile: GitHubProfile; keys: GitHubKey[] } | { ok: false; message: string };

/** "sam", "@sam" or a github.com/sam link → "sam". */
export const githubUserOf = (s: string) =>
  s
    .trim()
    .replace(/^(https?:\/\/)?(www\.)?github\.com\//i, "")
    .replace(/^@/, "")
    .replace(/[/?#].*$/, "");

/** The profile and its public keys. Never throws: a failure comes back as a plain message. */
export async function fromGitHub(user: string, fetcher: typeof fetch = fetch): Promise<GitHubAnswer> {
  if (!GITHUB_USER.test(user)) return { ok: false, message: "That isn't a GitHub username." };
  const get = (path: string) => fetcher(`https://api.github.com${path}`, { credentials: "omit", referrerPolicy: "no-referrer", cache: "no-store" });
  let p: Response, k: Response;
  try {
    [p, k] = await Promise.all([get(`/users/${user}`), get(`/users/${user}/keys?per_page=100`)]);
  } catch {
    return { ok: false, message: "We couldn't reach GitHub from this browser." };
  }
  if (p.status === 404) return { ok: false, message: `There's no GitHub account called @${user}.` };
  if (p.status === 403 || p.status === 429 || k.status === 403 || k.status === 429) {
    return { ok: false, message: "GitHub allows 60 lookups an hour from one network, and they're used up for now." };
  }
  if (!p.ok || !k.ok) return { ok: false, message: `GitHub answered ${p.ok ? k.status : p.status}.` };
  try {
    const pj = (await p.json()) as { login?: unknown; name?: unknown; avatar_url?: unknown };
    const kj = (await k.json()) as unknown;
    const login = typeof pj.login === "string" && GITHUB_USER.test(pj.login) ? pj.login : user;
    const avatar = typeof pj.avatar_url === "string" && pj.avatar_url.startsWith("https://avatars.githubusercontent.com/") ? pj.avatar_url : "";
    const keys = (Array.isArray(kj) ? kj : []).filter((x): x is GitHubKey => typeof x?.id === "number" && typeof x?.key === "string").slice(0, 100);
    return { ok: true, profile: { login, name: typeof pj.name === "string" && pj.name.trim() ? pj.name.slice(0, 100) : null, avatar }, keys };
  } catch {
    return { ok: false, message: "GitHub sent something we couldn't read." };
  }
}

/**
 * The key to tick first: the only one; else the one used most recently; else
 * the newest. None when there are several and GitHub gives no dates: the
 * owner picks.
 */
export function preselect<T extends { id: string; problem?: string; added?: string | null; lastUsed?: string | null }>(rows: T[]): string | null {
  const ok = rows.filter((x) => !x.problem);
  if (ok.length === 1) return ok[0]!.id;
  const t = (s?: string | null) => (s ? Date.parse(s) || 0 : 0);
  const used = [...ok].sort((a, b) => t(b.lastUsed) - t(a.lastUsed))[0];
  if (used && t(used.lastUsed)) return used.id;
  const added = [...ok].sort((a, b) => t(b.added) - t(a.added))[0];
  return added && t(added.added) ? added.id : null;
}
