import { fail } from "@sveltejs/kit";
import { migrate, sql, type Note } from "#lib/server/db.ts";
import type { Actions, PageServerLoad } from "./$types";

// The notes are awaited; the stats are not, so SvelteKit sends the page at
// once and streams the stats into it when the query finishes.
export const load: PageServerLoad = async () => {
  await migrate();
  const notes = await sql<Note[]>`select id, text, created_at::text from notes order by id desc limit 20`;
  const stats = sql<{ count: number; version: string }[]>`select count(*)::int as count, split_part(version(), ' ', 2) as version from notes`.then(
    ([row]) => ({ ...row, deploy: process.env.TIFFIN_DEPLOY ?? "local" }),
  );
  return { notes: [...notes], stats };
};

// A form action: a plain POST that works without JavaScript; use:enhance
// upgrades it to a fetch that keeps the page in place.
export const actions: Actions = {
  add: async ({ request }) => {
    const text = String((await request.formData()).get("text") ?? "").trim().slice(0, 280);
    if (!text) return fail(400, { error: "Write something first." });
    await migrate();
    await sql`insert into notes (text) values (${text})`;
    return { added: true };
  },
};
