import { MutationCache, QueryCache, QueryClient, queryOptions } from "@tanstack/react-query";
import { ApiError, api } from "./client";
import { forgetHistory } from "@/lib/command-history";
import { whoMap } from "@/lib/who";

/** Any 401 anywhere (except the login exchange itself) sends you to /login with a kind word. */
function on401(e: unknown) {
  if (e instanceof ApiError && e.status === 401 && !location.pathname.startsWith("/login")) {
    queryClient.clear();
    forgetHistory();
    const here = location.pathname + location.search;
    location.assign(`/login?reason=session${here !== "/" ? `&next=${encodeURIComponent(here)}` : ""}`);
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
    queryFn: async () => {
      const [tokens, people] = await Promise.all([api.tokens(true), api.people().catch(() => null)]);
      return whoMap(tokens, people ?? []);
    },
    staleTime: 60_000,
  }),
  project: (name: string) => queryOptions({ queryKey: ["project", name], queryFn: () => api.project(name), refetchInterval: 5_000 }),
  people: queryOptions({ queryKey: ["people"], queryFn: async () => (await api.people()) ?? [] }),
  passkeys: queryOptions({ queryKey: ["passkeys"], queryFn: async () => (await api.passkeys()) ?? [] }),
  manifest: (project: string) => queryOptions({ queryKey: ["manifest", project], queryFn: () => api.manifest(project), staleTime: 0 }),
  /** What the machine has and what uses it; a laptop dev server answers 503, so don't retry. */
  resources: queryOptions({
    queryKey: ["box-resources"],
    queryFn: api.boxResources,
    refetchInterval: (qq) => (qq.state.error ? false : 5_000),
    retry: false,
  }),
  secrets: (project: string) => queryOptions({ queryKey: ["secrets", project], queryFn: async () => (await api.secrets(project)) ?? [] }),
  /** A duplicate or import: polled every second while it runs. */
  projectJob: (id: string) =>
    queryOptions({
      queryKey: ["project-job", id],
      queryFn: () => api.projectJob(id),
      enabled: !!id,
      staleTime: 0,
      refetchInterval: (qq) => (qq.state.data?.status === "running" ? 1000 : false),
      refetchIntervalInBackground: true,
    }),
};
