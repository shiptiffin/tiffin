import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";
import type { components } from "./schema";

/** One dashboard sign-in: browser, how, where, when, and whether it is this browser. */
export type Session = components["schemas"]["Session"];
/** A browser the box recognises for someone: no new sign-in email from it. */
export type Browser = components["schemas"]["Browser"];

const who = (person?: string) => (person ? `person=${encodeURIComponent(person)}` : "");

export const sessions = {
  /** Open sessions; with history, also those that ended or expired in the last 30 days. */
  list: (person?: string, history = false) =>
    request<Session[] | null>("GET", `/v1/sessions?${[who(person), history ? "history=true" : ""].filter(Boolean).join("&")}`).then((x) => x ?? []),
  browsers: (person?: string) => request<Browser[] | null>("GET", `/v1/sessions/browsers?${who(person)}`).then((x) => x ?? []),
  end: (id: string) => request<Session>("DELETE", `/v1/sessions/${encodeURIComponent(id)}`),
  /** Everywhere but this browser (for someone else: all of theirs). */
  endOthers: (person?: string) => request<{ ended: number }>("POST", `/v1/sessions/end-others?${who(person)}`),
};

export const sq = {
  sessions: (person?: string) =>
    queryOptions({ queryKey: ["sessions", person ?? "me", "history"], queryFn: () => sessions.list(person, true), refetchInterval: 60_000 }),
  browsers: (person?: string) => queryOptions({ queryKey: ["sessions", person ?? "me", "browsers"], queryFn: () => sessions.browsers(person) }),
};

/** How a session signed in, in words. */
export const methodWords: Record<string, string> = {
  link: "Sign-in link",
  email: "Emailed link",
  passkey: "Passkey",
  google: "Google",
  github: "GitHub",
  terminal: "tiffin login",
};
