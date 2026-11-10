import type { APIRoute } from "astro";
export const prerender = false;

// Answers after `ms` milliseconds: an in-flight request for the shutdown test.
export const GET: APIRoute = async ({ url }) => {
  await new Promise((r) => setTimeout(r, Number(url.searchParams.get("ms") ?? 1000)));
  return new Response("slow done");
};
