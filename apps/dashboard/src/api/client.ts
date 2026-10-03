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
export type Token = S["Token"];
export type CreatedToken = S["CreatedToken"];
export type TokenCreateBody = S["TokenCreateBody"];
export type ApplyResult = S["ApplyResult"];
export type Approval = S["Approval"];
export type Passkey = S["Passkey"];
export type ProjectState = S["ProjectState"];
export type ResourceStatus = S["ResourceStatus"];
export type SecretInfo = S["SecretInfo"];
export type Person = S["Person"];
export type Invite = S["Invite"];
export type Role = Person["role"];
export type Tier = "read" | "reversible" | "outbound" | "irreversible";

/** An RFC 9457 problem from the API, as a throwable error. */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem;
  constructor(problem: Problem) {
    super(problem.detail || problem.title);
    this.name = "ApiError";
    this.status = problem.status;
    this.problem = problem;
  }
}

type Path = keyof paths;

async function request<T>(method: string, path: Path | string, body?: unknown): Promise<T> {
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
    throw new ApiError({
      status: res.status,
      code: p.code ?? "internal",
      title: p.title ?? res.statusText,
      detail: p.detail,
      hint: p.hint,
      errors: p.errors,
      plan: p.plan,
      approval: p.approval,
      approvalUrl: p.approvalUrl,
    });
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
  tokens: (revoked = false) => request<Token[]>("GET", `/v1/tokens${revoked ? "?revoked=true" : ""}`),
  createToken: (body: TokenCreateBody) => request<CreatedToken>("POST", "/v1/tokens", body),
  revokeToken: (id: string) => request<void>("DELETE", `/v1/tokens/${encodeURIComponent(id)}`),
  project: (name: string) => request<ProjectState>("GET", `/v1/projects/${encodeURIComponent(name)}`),
  approvals: (status?: Approval["status"]) => request<Approval[] | null>("GET", `/v1/approvals${status ? `?status=${status}` : ""}`),
  approval: (id: string) => request<Approval>("GET", `/v1/approvals/${encodeURIComponent(id)}`),
  /** WebAuthn assertion options bound to this approval, as go-webauthn emits them. */
  beginApproval: (id: string) => request<unknown>("POST", `/v1/approvals/${encodeURIComponent(id)}/begin`),
  approve: (id: string, credential: unknown) => request<Approval>("POST", `/v1/approvals/${encodeURIComponent(id)}/approve`, { credential }),
  reject: (id: string, reason: string) => request<Approval>("POST", `/v1/approvals/${encodeURIComponent(id)}/reject`, reason ? { reason } : {}),
  passkeys: () => request<Passkey[] | null>("GET", "/v1/passkeys"),
  beginPasskey: () => request<unknown>("POST", "/v1/passkeys/register"),
  addPasskey: (name: string, credential: unknown) => request<Passkey>("POST", "/v1/passkeys", { name, credential }),
  deletePasskey: (id: string) => request<void>("DELETE", `/v1/passkeys/${encodeURIComponent(id)}`),
  secrets: (project: string) => request<SecretInfo[] | null>("GET", `/v1/projects/${encodeURIComponent(project)}/secrets`),
  setSecret: (project: string, name: string, value: string) =>
    request<unknown>("PUT", `/v1/projects/${encodeURIComponent(project)}/secrets/${encodeURIComponent(name)}`, { value }),
  deleteSecret: (project: string, name: string) =>
    request<void>("DELETE", `/v1/projects/${encodeURIComponent(project)}/secrets/${encodeURIComponent(name)}`),
  people: () => request<Person[] | null>("GET", "/v1/people"),
  invite: (body: { name: string; email?: string; role: Exclude<Role, "owner"> }) => request<Invite>("POST", "/v1/people", body),
  updatePerson: (id: string, body: { name?: string; role?: Exclude<Role, "owner"> }) =>
    request<Person>("PATCH", `/v1/people/${encodeURIComponent(id)}`, body),
  removePerson: (id: string) => request<void>("DELETE", `/v1/people/${encodeURIComponent(id)}`),
  personLink: (id: string) => request<Invite>("POST", `/v1/people/${encodeURIComponent(id)}/login-link`),
  login: (code: string) => request<Principal>("POST", "/v1/session", { code }),
  logout: () => request<void>("DELETE", "/v1/session"),
};

export function isProblem(e: unknown, ...codes: Problem["code"][]): e is ApiError {
  return e instanceof ApiError && (codes.length === 0 || codes.includes(e.problem.code));
}

/** True when the box runs without --box, so approvals, passkeys and secrets don't exist here. */
export function notOnBox(e: unknown) {
  return e instanceof ApiError && e.status === 501;
}
