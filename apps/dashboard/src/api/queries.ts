import { MutationCache, QueryCache, QueryClient, queryOptions } from "@tanstack/react-query";
import { ApiError, api } from "./client";

/** Any 401 anywhere (except the login exchange itself) sends you to /login with a kind word. */
function on401(e: unknown) {
  if (e instanceof ApiError && e.status === 401 && !location.pathname.startsWith("/login")) {
    queryClient.clear();
    location.assign("/login?reason=session");
  }
}

export const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: on401 }),
  mutationCache: new MutationCache({ onError: on401 }),
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      retry: (n, e) => !(e instanceof ApiError && e.status >= 400 && e.status < 500) && n < 2,
    },
  },
});

export const q = {
  whoami: queryOptions({ queryKey: ["whoami"], queryFn: api.whoami, staleTime: 60_000 }),
  projects: queryOptions({ queryKey: ["projects"], queryFn: api.projects }),
  changes: (project?: string) => queryOptions({ queryKey: ["changes", project ?? ""], queryFn: () => api.changes(project) }),
  change: (id: string) => queryOptions({ queryKey: ["change", id], queryFn: () => api.change(id) }),
  status: (refetchInterval = 30_000) =>
    queryOptions({ queryKey: ["status"], queryFn: api.status, refetchInterval, refetchIntervalInBackground: false }),
  tokens: queryOptions({ queryKey: ["tokens"], queryFn: () => api.tokens() }),
  /** Every token including revoked ones, to put names to token IDs. */
  tokenNames: queryOptions({
    queryKey: ["tokens", "names"],
    queryFn: async () => new Map((await api.tokens(true)).map((t) => [t.id, t] as const)),
    staleTime: 60_000,
  }),
  project: (name: string) => queryOptions({ queryKey: ["project", name], queryFn: () => api.project(name), refetchInterval: 5_000 }),
  pending: queryOptions({ queryKey: ["approvals", "pending"], queryFn: async () => (await api.approvals("pending")) ?? [], refetchInterval: 15_000 }),
  approvals: queryOptions({ queryKey: ["approvals", "all"], queryFn: async () => (await api.approvals()) ?? [], refetchInterval: 15_000 }),
  approval: (id: string) => queryOptions({ queryKey: ["approval", id], queryFn: () => api.approval(id), refetchInterval: 10_000 }),
  people: queryOptions({ queryKey: ["people"], queryFn: async () => (await api.people()) ?? [] }),
  passkeys: queryOptions({ queryKey: ["passkeys"], queryFn: async () => (await api.passkeys()) ?? [] }),
  secrets: (project: string) => queryOptions({ queryKey: ["secrets", project], queryFn: async () => (await api.secrets(project)) ?? [] }),
};
