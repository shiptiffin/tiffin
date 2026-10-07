// Every request the engine makes goes to a sign-in provider: OpenID Connect
// discovery, token exchange, user info, JWKS. A project's own OpenID Connect
// issuer is whatever URL its developer set, and every project shares this one
// process, so each answer is bounded: a deadline that covers reading the
// body, and a size cap enforced while it streams in, before anything buffers
// or parses it. A huge or endless answer fails that one sign-in, never the
// engine.

/** The most a provider's answer may be. Discovery documents and JWKS are a few KB. */
export const MAX_RESPONSE_BYTES = 1 << 20;
/** How long a provider has to answer in full. */
export const FETCH_TIMEOUT_MS = 15_000;

const NULL_BODY = new Set([101, 204, 205, 304]);

/** `inner` with a deadline and a response size cap. The answer comes back fully read. */
export function boundedFetch(inner: typeof fetch, maxBytes = MAX_RESPONSE_BYTES, timeoutMs = FETCH_TIMEOUT_MS): typeof fetch {
  const bounded = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const deadline = AbortSignal.timeout(timeoutMs);
    const signal = init?.signal ? AbortSignal.any([init.signal, deadline]) : deadline;
    const res = await inner(input, { ...init, signal });
    const tooBig = () => new Error(`auth engine: a provider's answer from ${describe(input)} is over ${maxBytes} bytes`);
    const declared = Number(res.headers.get("content-length"));
    if (Number.isFinite(declared) && declared > maxBytes) {
      await res.body?.cancel().catch(() => {});
      throw tooBig();
    }
    const headers = new Headers(res.headers);
    // The body below is already decoded: its length and encoding are new.
    headers.delete("content-length");
    headers.delete("content-encoding");
    if (!res.body || NULL_BODY.has(res.status)) {
      return new Response(null, { status: res.status, statusText: res.statusText, headers });
    }
    const reader = res.body.getReader();
    const chunks: Uint8Array[] = [];
    let total = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maxBytes) {
        await reader.cancel().catch(() => {});
        throw tooBig();
      }
      chunks.push(value);
    }
    const body = new Uint8Array(total);
    let at = 0;
    for (const c of chunks) {
      body.set(c, at);
      at += c.byteLength;
    }
    return new Response(body, { status: res.status, statusText: res.statusText, headers });
  };
  return Object.assign(bounded, { preconnect: (inner as { preconnect?: unknown }).preconnect }) as typeof fetch;
}

function describe(input: RequestInfo | URL): string {
  try {
    const u = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    return u.origin;
  } catch {
    return "a provider";
  }
}

let installed = false;

/**
 * Bounds every fetch in the process. Better Auth and the libraries under it
 * (better-fetch, jose) look up the global fetch when they call it.
 */
export function installFetchLimits(): void {
  if (installed) return;
  installed = true;
  globalThis.fetch = boundedFetch(globalThis.fetch);
}
