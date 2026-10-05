import type { components, paths } from "./schema";

type S = components["schemas"];
export type Change = S["Change"];
export type Plan = S["Plan"];
export type Op = S["Op"];
export type Actor = S["Actor"];
export type Problem = S["Problem"];
export type Principal = S["Principal"];
export type ProjectSummary = S["ProjectSummary"];
export type StatusReport = S["StatusReport"];
export type Check = S["Check"];
/** An API key (GET /v1/tokens): which projects, full or read access, when it expires. */
export type Token = S["Key"];
export type Key = S["Key"];
export type CreatedToken = S["CreatedKey"];
export type KeyCreateBody = S["KeyCreateBody"];
export type ApplyResult = S["ApplyResult"];
export type Passkey = S["Passkey"];
export type ProjectState = S["ProjectState"];
export type ResourceStatus = S["ResourceStatus"];
export type SecretInfo = S["SecretInfo"];
export type Person = S["Person"];
export type Invite = S["Invite"];
export type Role = Person["role"];
export type Tier = "read" | "reversible" | "outbound" | "irreversible";
export type Appearance = S["Appearance"];
export type Manifest = S["Manifest"];
export type ManifestApp = S["ManifestApp"];
export type ProjectManifest = S["ProjectManifest"];
export type BoxResources = S["BoxResources"];
export type BoxServer = S["BoxServer"];
export type ServerOffer = S["ServerOffer"];
export type BoxService = S["BoxService"];
export type BoxApp = S["BoxApp"];
export type ProjectJob = S["PortableProjectJob"];
export type ProjectExport = S["PortableProjectExport"];
export type ArchiveSummary = S["PortableArchiveSummary"];

/** An RFC 9457 problem from the API, as a throwable error. */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem & { confirm?: string; preview?: unknown };
  constructor(problem: Problem & { confirm?: string; preview?: unknown }) {
    super(problem.detail || problem.title);
    this.name = "ApiError";
    this.status = problem.status;
    this.problem = problem;
  }
}

type Path = keyof paths;

export async function request<T>(method: string, path: Path | string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: body === undefined ? { Accept: "application/json" } : { Accept: "application/json", "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError({
      status: 0,
      code: "internal",
      title: "Offline",
      detail: "Can't reach the box.",
      hint: "Check that `tiffin serve` is running, then try again.",
    });
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (!res.ok) {
    const p = (data && typeof data === "object" ? data : {}) as Partial<Problem>;
    // Keep every field: some problems carry extras (confirm, preview).
    throw new ApiError({ ...p, status: res.status, code: p.code ?? "internal", title: p.title ?? res.statusText });
  }
  return data as T;
}

export const api = {
  whoami: () => request<Principal>("GET", "/v1/whoami"),
  projects: () => request<ProjectSummary[]>("GET", "/v1/projects"),
  changes: (project?: string, limit = 200) =>
    request<Change[]>("GET", `/v1/changes?limit=${limit}${project ? `&project=${encodeURIComponent(project)}` : ""}`),
  change: (id: string) => request<Change>("GET", `/v1/changes/${encodeURIComponent(id)}`),
  /** Without confirm the API answers 428 with the undo plan (thrown as ApiError). */
  undo: (id: string, confirm?: string) => request<ApplyResult>("POST", `/v1/changes/${encodeURIComponent(id)}/undo`, confirm ? { confirm } : {}),
  status: () => request<StatusReport>("GET", "/v1/status"),
  tokens: (revoked = false) => request<Token[] | null>("GET", `/v1/tokens${revoked ? "?revoked=true" : ""}`).then((x) => x ?? []),
  createToken: (body: KeyCreateBody) => request<CreatedToken>("POST", "/v1/tokens", body),
  revokeToken: (id: string) => request<void>("DELETE", `/v1/tokens/${encodeURIComponent(id)}`),
  project: (name: string) => request<ProjectState>("GET", `/v1/projects/${encodeURIComponent(name)}`),
  passkeys: () => request<Passkey[] | null>("GET", "/v1/passkeys"),
  beginPasskey: () => request<unknown>("POST", "/v1/passkeys/register"),
  addPasskey: (name: string, credential: unknown) => request<Passkey>("POST", "/v1/passkeys", { name, credential }),
  deletePasskey: (id: string) => request<void>("DELETE", `/v1/passkeys/${encodeURIComponent(id)}`),
  secrets: (project: string) => request<SecretInfo[] | null>("GET", `/v1/projects/${encodeURIComponent(project)}/secrets`),
  /** A change in History (`change`, absent when the value was already this); undo puts the old value back. */
  setSecret: (project: string, name: string, value: string) =>
    request<{ change?: string }>("PUT", `/v1/projects/${encodeURIComponent(project)}/secrets/${encodeURIComponent(name)}`, { value }),
  deleteSecret: (project: string, name: string) =>
    request<{ change?: string }>("DELETE", `/v1/projects/${encodeURIComponent(project)}/secrets/${encodeURIComponent(name)}`),
  people: () => request<Person[] | null>("GET", "/v1/people"),
  invite: (body: { name: string; email?: string; role: Exclude<Role, "owner"> }) => request<Invite>("POST", "/v1/people", body),
  updatePerson: (id: string, body: { name?: string; role?: Exclude<Role, "owner"> }) =>
    request<Person>("PATCH", `/v1/people/${encodeURIComponent(id)}`, body),
  removePerson: (id: string) => request<void>("DELETE", `/v1/people/${encodeURIComponent(id)}`),
  personLink: (id: string) => request<Invite>("POST", `/v1/people/${encodeURIComponent(id)}/login-link`),
  appearance: (project: string) => request<Appearance>("GET", `/v1/projects/${encodeURIComponent(project)}/appearance`),
  setAppearance: (project: string, enamel: Appearance["enamel"]) =>
    request<Appearance>("PUT", `/v1/projects/${encodeURIComponent(project)}/appearance`, { enamel }),
  manifest: (project: string) => request<ProjectManifest>("GET", `/v1/projects/${encodeURIComponent(project)}/manifest`),
  renderConfig: (manifest: Manifest) => request<{ config: string }>("POST", "/v1/manifest/render", { manifest }),
  plan: (manifest: Manifest) => request<Plan>("POST", "/v1/plan", { manifest }),
  /** Applies when `confirm` matches the plan hash; otherwise 428 with the plan (thrown as ApiError). */
  apply: (manifest: Manifest, confirm: string, intent?: string) =>
    request<ApplyResult>("POST", "/v1/apply", { manifest, confirm, ...(intent ? { intent } : {}) }),
  boxResources: () => request<BoxResources>("GET", "/v1/box/resources"),
  /** WebAuthn options for signing in with any passkey registered on this box. */
  passkeyOptions: () => request<unknown>("POST", "/v1/session/passkey/options"),
  /** Signs in with a passkey assertion; sets the session cookie like a login link. */
  passkeyLogin: (credential: unknown) => request<Principal>("POST", "/v1/session/passkey", { credential }),
  login: (code: string) => request<Principal>("POST", "/v1/session", { code }),
  logout: () => request<void>("DELETE", "/v1/session"),
  /** Copies a project under a new name; poll projectJob until done or failed. */
  duplicate: (project: string, name: string) => request<ProjectJob>("POST", `/v1/projects/${encodeURIComponent(project)}/duplicate`, { name }),
  projectJob: (id: string) => request<ProjectJob>("GET", `/v1/project-jobs/${encodeURIComponent(id)}`),
  /** Records an export; GET its `download` path to make the file as it downloads. */
  exportProject: (project: string, body: { includeSecrets: boolean; withHistory: boolean }) =>
    request<ProjectExport>("POST", `/v1/projects/${encodeURIComponent(project)}/exports`, body),
  projectExport: (project: string, id: string) => request<ProjectExport>("GET", `/v1/projects/${encodeURIComponent(project)}/exports/${encodeURIComponent(id)}`),
  applyImport: (id: string, body: { name?: string; secretsKey?: string; withoutSecrets?: boolean }) =>
    request<ProjectJob>("POST", `/v1/project-imports/${encodeURIComponent(id)}/apply`, body),
  discardImport: (id: string) => request<ProjectJob>("DELETE", `/v1/project-imports/${encodeURIComponent(id)}`),
  startProject: (project: string) => request<ApplyResult>("POST", `/v1/projects/${encodeURIComponent(project)}/start`, {}),
};

/** Uploads a file as the raw body, reporting progress (fetch can't report upload progress). */
export function uploadFile<T>(path: string, file: File, onProgress: (sent: number) => void): Promise<T> {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open("POST", path);
    x.setRequestHeader("Content-Type", "application/octet-stream");
    x.setRequestHeader("Accept", "application/json");
    x.upload.onprogress = (ev) => onProgress(ev.loaded);
    x.onload = () => {
      let data: unknown = {};
      try {
        data = JSON.parse(x.responseText || "{}");
      } catch {
        /* not JSON */
      }
      if (x.status >= 200 && x.status < 300) resolve(data as T);
      else {
        const p = data as Partial<Problem>;
        reject(new ApiError({ ...p, status: x.status, code: p.code ?? "internal", title: p.title ?? x.statusText }));
      }
    };
    x.onerror = () => reject(new ApiError({ status: 0, code: "internal", title: "Offline", detail: "The upload stopped: can't reach the box." }));
    x.send(file);
  });
}

export function isProblem(e: unknown, ...codes: Problem["code"][]): e is ApiError {
  return e instanceof ApiError && (codes.length === 0 || codes.includes(e.problem.code));
}

/** True when the box runs without --box, so passkeys and secrets don't exist here. */
export function notOnBox(e: unknown) {
  return e instanceof ApiError && e.status === 501;
}
