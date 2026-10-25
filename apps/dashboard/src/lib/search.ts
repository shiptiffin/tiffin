import { queryOptions } from "@tanstack/react-query";
import { request } from "@/api/client";
import type { components } from "@/api/schema";

type S = components["schemas"];
export type SearchStatus = S["SearchStatus"];
export type SearchHost = S["SearchHostStatus"];
export type SearchPing = S["SearchPing"];
export type Crawler = S["SearchCrawler"];

const enc = encodeURIComponent;
const path = (p: string) => `/v1/projects/${enc(p)}/search`;

/** How a project's addresses stand with search engines. Polls while a check waits. */
export const searchQuery = (project: string) =>
  queryOptions({
    queryKey: ["search", project],
    queryFn: () => request<SearchStatus>("GET", path(project)),
    retry: false,
    refetchInterval: (qq) => (qq.state.error ? false : (qq.state.data?.hosts ?? []).some((h) => h.pending) ? 10_000 : 60_000),
  });

/** Checks a host now and tells search engines what changed (all: every page again). */
export const pingSearch = (project: string, host: string, all = false) => request<SearchStatus>("POST", `${path(project)}/ping`, { host, all: all || undefined });

export const setIndexNow = (project: string, indexNow: boolean) => request<SearchStatus>("PUT", `${path(project)}/settings`, { indexNow });

/** The names nobody ranks on, as the box decides (platform.HiddenFromSearch). */
export function hiddenFromSearch(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, "");
  const under = (z: string) => h === z || h.endsWith(`.${z}`);
  return under("shiptiffin.app") || under("sslip.io") || under("localhost") || [".local", ".internal", ".lan", ".home.arpa", ".invalid"].some((s) => h.endsWith(s));
}

export const reasonWords: Record<string, string> = {
  live: "when the domain went live",
  deploy: "after a deploy",
  check: "at the regular check",
  manual: "when asked",
};

export const purposeWords: Record<string, string> = {
  search: "search index",
  user: "fetched for a chat",
  training: "AI training",
};
