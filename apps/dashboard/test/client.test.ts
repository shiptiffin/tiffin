import { expect, test } from "bun:test";
import { ApiError, request } from "@/api/client";

test("a cancelled read is a cancellation, not Offline", async () => {
  const real = globalThis.fetch;
  globalThis.fetch = ((_: unknown, init?: RequestInit) =>
    new Promise((_, reject) => init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))))) as typeof fetch;
  try {
    const ac = new AbortController();
    const p = request("GET", "/v1/projects", undefined, ac.signal);
    ac.abort();
    const err = await p.catch((e: unknown) => e);
    expect(err).not.toBeInstanceOf(ApiError);
    expect((err as Error).name).toBe("AbortError");
    // A real network failure still reads as Offline.
    globalThis.fetch = (() => Promise.reject(new TypeError("Failed to fetch"))) as unknown as typeof fetch;
    const off = await request("GET", "/v1/projects").catch((e: unknown) => e);
    expect(off).toBeInstanceOf(ApiError);
    expect((off as ApiError).status).toBe(0);
  } finally {
    globalThis.fetch = real;
  }
});
