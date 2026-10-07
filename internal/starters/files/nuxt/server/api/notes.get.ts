export default defineEventHandler(async () => {
  await migrate();
  const notes = await sql<Note[]>`select id, text, created_at::text from notes order by id desc limit 20`;
  const [{ count, version }] = await sql<{ count: number; version: string }[]>`select count(*)::int as count, split_part(version(), ' ', 2) as version from notes`;
  return { notes: [...notes], count, version, deploy: process.env.TIFFIN_DEPLOY ?? "local" };
});
