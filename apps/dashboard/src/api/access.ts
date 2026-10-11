import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";

// Hand-written to match the box's contract until schema.d.ts is regenerated
// (bun run gen:api) with these endpoints in it.

/** GET /v1/session/box (no sign-in needed): whether ShipTiffin manages this box, and the host to ssh to. */
export type SessionBox = { managed: boolean; host: string };

/**
 * GET /v1/me/recovery: the ways the signed-in person could get back in once
 * this session ends. sshKeys counts the owner's server keys; email is the
 * box's mail relay (emailed sign-in links); providers, Google or GitHub
 * sign-in. firstPasskeyOpen: this session may add a first passkey without
 * "confirm it's you" right now.
 */
export type Recovery = { passkeys: number; sshKeys: number; email: boolean; providers: number; firstPasskeyOpen: boolean };

/** A key that can log in to the server as root. source: "pasted", "hetzner", "dashboard" or "github:<user>". */
export type ServerKey = { type: string; fingerprint: string; bits?: number; source: string; added: string };

/** GET /v1/server-access (owner only): the keys, the host and the file they live in. */
export type ServerAccess = { keys: ServerKey[] | null; host: string; file: string };

export const access = {
  box: () => request<SessionBox>("GET", "/v1/session/box"),
  recovery: () => request<Recovery>("GET", "/v1/me/recovery"),
  serverAccess: () => request<ServerAccess>("GET", "/v1/server-access"),
  /** One or more public key lines. Needs a recent strong sign-in (reauth_required otherwise); a bad key answers 422 with why. */
  addKeys: (keys: string, source: "pasted" | `github:${string}`) => request<ServerAccess>("POST", "/v1/server-access/keys", { keys, source }),
  /** Removing a key doesn't end SSH sessions already open with it. */
  removeKey: (fingerprint: string) => request<ServerAccess>("POST", "/v1/server-access/keys/remove", { fingerprint }),
};

export const aq = {
  box: queryOptions({ queryKey: ["session-box"], queryFn: access.box, staleTime: 300_000, retry: false }),
  recovery: queryOptions({ queryKey: ["recovery"], queryFn: access.recovery, staleTime: 60_000, retry: false }),
  serverAccess: queryOptions({ queryKey: ["server-access"], queryFn: access.serverAccess, retry: false }),
};
