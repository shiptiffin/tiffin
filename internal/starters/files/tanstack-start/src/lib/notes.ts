import { createServerFn } from "@tanstack/react-start";
import { db, migrate } from "./db.server";

export type Note = { id: number; text: string; created_at: string };

// Server functions: their bodies only run on the server; the browser calls them over HTTP.
export const listNotes = createServerFn({ method: "GET" }).handler(async () => {
  await migrate();
  const notes = await db()<Note[]>`select id, text, created_at::text from notes order by id desc limit 20`;
  return [...notes];
});

// Slower, non-critical numbers: the page streams them in after the notes.
export const noteStats = createServerFn({ method: "GET" }).handler(async () => {
  await migrate();
  const [{ count, version }] = await db()<{ count: number; version: string }[]>`select count(*)::int as count, split_part(version(), ' ', 2) as version from notes`;
  return { count, version, deploy: process.env.TIFFIN_DEPLOY ?? "local" };
});

export const addNote = createServerFn({ method: "POST" })
  .validator((text: unknown) => {
    const t = String(text ?? "").trim().slice(0, 280);
    if (!t) throw new Error("Write something first.");
    return t;
  })
  .handler(async ({ data }) => {
    await migrate();
    await db()`insert into notes (text) values (${data})`;
  });
