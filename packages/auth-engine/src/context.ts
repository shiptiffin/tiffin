// Per-request facts the engine learns before Better Auth runs, shared with
// hooks deeper in the call (which only see the user, not the request).
import { AsyncLocalStorage } from "node:async_hooks";

export type RequestFacts = {
  /** Set when the request authenticates with an API key (x-api-key). */
  apiKey?: { id: string; userId: string; maxRole: string | null };
};

export const requestFacts = new AsyncLocalStorage<RequestFacts>();

export function currentFacts(): RequestFacts {
  return requestFacts.getStore() ?? {};
}
