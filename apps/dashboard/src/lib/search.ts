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

export type SearchSettings = S["SearchSettings"];
export const setSearchSettings = (project: string, settings: SearchSettings) => request<SearchStatus>("PUT", `${path(project)}/settings`, settings);

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

export type GoogleConnection = S["SearchGoogleConnection"];
export type GoogleHost = S["SearchGoogleHost"];

/** The box's Google service account (box admins; a 403 means "not yours"). */
export const googleQuery = queryOptions({
  queryKey: ["search-google"],
  queryFn: () => request<GoogleConnection>("GET", "/v1/search/google"),
  retry: false,
  refetchInterval: (qq) => ((qq.state.data?.properties ?? []).some((p) => p.state === "verifying") ? 5_000 : false),
});

export const connectGoogle = (key: string, owner: string) => request<GoogleConnection>("PUT", "/v1/search/google", { key, owner: owner || undefined });
export const disconnectGoogle = () => request<GoogleConnection>("DELETE", "/v1/search/google");

export const GOOGLE_CONSOLE = {
  project: "https://console.cloud.google.com/projectcreate",
  searchApi: "https://console.cloud.google.com/apis/library/searchconsole.googleapis.com",
  verifyApi: "https://console.cloud.google.com/apis/library/siteverification.googleapis.com",
  accounts: "https://console.cloud.google.com/iam-admin/serviceaccounts/create",
};

export type IndexNowStatus = S["SearchIndexNowStatus"];

/** IndexNow across the box: every findable address and its last ping (box admins). */
export const indexNowQuery = queryOptions({
  queryKey: ["search-indexnow"],
  queryFn: () => request<IndexNowStatus>("GET", "/v1/search/indexnow"),
  retry: false,
});

export type SearchReport = S["SearchReport"];
export type ReportQuery = { period?: string; from?: string; to?: string; host?: string };

/** Analytics › Search: Google's numbers for some days, the engines' state and the crawlers. */
export const reportQuery = (project: string, q: ReportQuery) =>
  queryOptions({
    queryKey: ["search", project, "report", q],
    queryFn: () => {
      const p = new URLSearchParams(Object.entries(q).filter(([, v]) => v) as [string, string][]);
      const qs = p.size ? `?${p}` : "";
      return request<SearchReport>("GET", `${path(project)}/report${qs}`);
    },
    retry: false,
    refetchInterval: (qq) => (qq.state.data?.google.state === "verifying" || qq.state.data?.google.filling ? 15_000 : 300_000),
  });
