export default defineEventHandler(async () => {
  await migrate();
  const [notes, [{ count, version }]] = await Promise.all([
    sql<Note[]>`select id, text, created_at::text from notes order by id desc limit 20`,
    sql<{ count: number; version: string }[]>`select count(*)::int as count, split_part(version(), ' ', 2) as version from notes`,
  ]);
  return { notes: [...notes], count, version, deploy: process.env.TIFFIN_DEPLOY ?? "local" };
});
