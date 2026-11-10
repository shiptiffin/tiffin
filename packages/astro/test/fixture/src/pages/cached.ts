import type { APIRoute } from "astro";
export const prerender = false;

let renders = 0;

// Cached for a minute under the tag "fixture".
export const GET: APIRoute = ({ cache }) => {
  cache.set({ maxAge: 60, tags: ["fixture"] });
  renders++;
  return Response.json({ renders, pid: process.pid, at: Date.now() });
};
