import type { APIRoute } from "astro";
export const prerender = false;

// What Astro thinks the request URL is (proxy trust).
export const GET: APIRoute = ({ url, site }) => Response.json({ url: url.href, site: site?.href ?? null });
