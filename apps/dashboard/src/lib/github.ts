import { queryOptions } from "@tanstack/react-query";
import { request } from "@/api/client";
import type { components } from "@/api/schema";

type S = components["schemas"];
export type GitHubStatus = S["RuntimeGitHubStatus"];
export type GitHubRepo = S["RuntimeGitHubRepo"];
export type GitHubRepoList = S["RuntimeGitHubRepoList"];
export type GitHubRepoDetail = S["RuntimeGitHubRepoDetail"];
export type RepoRoot = S["RuntimeRepoRoot"];
export type GitHubEvent = S["RuntimeGitHubEvent"];
type Deploy = S["RuntimeDeploy"];

/** The box's GitHub connection. Cheap to ask; the box calls GitHub briefly. */
export const githubQuery = queryOptions({
  queryKey: ["github"],
  queryFn: () => request<GitHubStatus>("GET", "/v1/github"),
  staleTime: 15_000,
  retry: false,
});

export const reposQuery = (refresh = false) =>
  queryOptions({
    queryKey: ["github-repos"],
    queryFn: () => request<GitHubRepoList>("GET", `/v1/github/repos?limit=1000${refresh ? "&refresh=true" : ""}`),
    staleTime: 60_000,
    retry: false,
  });

export const repoQuery = (fullName: string, branch?: string) =>
  queryOptions({
    queryKey: ["github-repo", fullName, branch ?? ""],
    queryFn: () => request<GitHubRepoDetail>("GET", `/v1/github/repos/${fullName.split("/").map(encodeURIComponent).join("/")}${branch ? `?branch=${encodeURIComponent(branch)}` : ""}`),
    staleTime: 60_000,
    retry: false,
    enabled: !!fullName,
  });

/**
 * Connect GitHub: the box returns a manifest that the browser POSTs to
 * GitHub as a form (GitHub's manifest flow needs a real form post), so this
 * leaves the page. GitHub brings the person back to Settings › Git.
 */
export async function connectGitHub(org?: string) {
  const start = await request<{ url: string; manifest: string; name: string }>("POST", "/v1/github/connect", org ? { org } : {});
  const form = document.createElement("form");
  form.method = "POST";
  form.action = start.url;
  const field = document.createElement("input");
  field.type = "hidden";
  field.name = "manifest";
  field.value = start.manifest;
  form.appendChild(field);
  document.body.appendChild(form);
  form.submit();
}

/** Opens GitHub's page for picking the accounts and repositories the app may see. */
export async function installGitHub() {
  const r = await request<{ url: string }>("POST", "/v1/github/install", {});
  location.assign(r.url);
}

export const disconnectGitHub = () => request<{ url?: string }>("DELETE", "/v1/github");

const appPath = (p: string, app: string) => `/v1/projects/${encodeURIComponent(p)}/apps/${encodeURIComponent(app)}`;

/** Deploys an app's production branch (or ref) from GitHub now: Redeploy. */
export const deployGitHub = (project: string, app: string, ref?: string) => request<Deploy>("POST", `${appPath(project, app)}/deploys/github`, ref ? { ref } : {});

/** Saves a project secret (env var, encrypted on the box). */
export const setSecret = (project: string, name: string, value: string) =>
  request("PUT", `/v1/projects/${encodeURIComponent(project)}/secrets/${encodeURIComponent(name)}`, { value });

/** "acme/shop" → "shop", as a project or app name. */
export function nameFromRepo(fullName: string): string {
  const n = (fullName.split("/")[1] ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^[^a-z]+/, "")
    .replace(/-+/g, "-")
    .replace(/-$/, "")
    .slice(0, 40);
  return n;
}

/** The short commit: seven characters, like GitHub shows. */
export const shortSha = (sha?: string) => (sha ? sha.slice(0, 7) : "");

/** Environment variable names the box accepts as secrets. */
export const ENV_NAME = /^[A-Z_][A-Z0-9_]{0,127}$/;

/** Parses pasted .env text into rows (comments and blanks skipped, quotes stripped). */
export function parseEnv(text: string): Array<{ k: string; v: string }> {
  const out: Array<{ k: string; v: string }> = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const m = line.replace(/^export\s+/, "").match(/^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/);
    if (!m) continue;
    let v = m[2].trim();
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) v = v.slice(1, -1);
    out.push({ k: m[1].toUpperCase(), v });
  }
  return out;
}
