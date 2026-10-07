// Takes JSON from the page's script, or a plain form POST (no JavaScript),
// which it answers with a redirect back to the page.
export default defineEventHandler(async (event) => {
  const form = getRequestHeader(event, "content-type")?.includes("application/x-www-form-urlencoded");
  const body = form ? Object.fromEntries(new URLSearchParams(await readRawBody(event) ?? "")) : await readBody<{ text?: string }>(event);
  const text = String(body?.text ?? "").trim().slice(0, 280);
  if (!text) throw createError({ statusCode: 400, statusMessage: "Write something first." });
  await migrate();
  await sql`insert into notes (text) values (${text})`;
  if (form) return sendRedirect(event, "/", 303);
  return { ok: true };
});
