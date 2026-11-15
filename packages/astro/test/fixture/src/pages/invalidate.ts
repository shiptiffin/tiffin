import type { APIRoute } from "astro";
export const prerender = false;

export const POST: APIRoute = async ({ cache }) => {
  await cache.invalidate({ tags: ["fixture"] });
  return Response.json({ ok: true });
};
