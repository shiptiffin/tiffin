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
  tokens: () => request<Token[]>("GET", "/v1/tokens"),
  createToken: (body: TokenCreateBody) => request<CreatedToken>("POST", "/v1/tokens", body),
  revokeToken: (id: string) => request<void>("DELETE", `/v1/tokens/${encodeURIComponent(id)}`),
  login: (code: string) => request<Principal>("POST", "/v1/session", { code }),
  logout: () => request<void>("DELETE", "/v1/session"),
};

export function isProblem(e: unknown, ...codes: Problem["code"][]): e is ApiError {
  return e instanceof ApiError && (codes.length === 0 || codes.includes(e.problem.code));
}
