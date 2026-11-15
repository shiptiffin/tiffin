import type { APIRoute } from "astro";
export const prerender = false;

// Counts visits in the session: proves the session round-trips through the driver.
export const GET: APIRoute = async ({ session }) => {
  const count = ((await session?.get("count")) ?? 0) + 1;
  session?.set("count", count);
  return Response.json({ count, id: session?.sessionID ?? null });
};
