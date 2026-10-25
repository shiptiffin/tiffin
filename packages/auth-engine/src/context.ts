// Per-request facts the engine learns before Better Auth runs, shared with
// hooks deeper in the call (which only see the user, not the request).
import { AsyncLocalStorage } from "node:async_hooks";

export type RequestFacts = {
  /** The app host the request is for (no port): its Host, or x-tiffin-host on internal calls. */
  host?: string;
  /** Set while the request creates an account. */
  newUser?: boolean;
  /** The account the request made, once it is saved (reported to analytics). */
  newUserId?: string;
  /** Set when the request authenticates with an API key (x-api-key). */
  apiKey?: { id: string; userId: string; maxRole: string | null };
};

export const requestFacts = new AsyncLocalStorage<RequestFacts>();

export function currentFacts(): RequestFacts {
  return requestFacts.getStore() ?? {};
}
