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
  tokens: queryOptions({ queryKey: ["tokens"], queryFn: api.tokens }),
};
