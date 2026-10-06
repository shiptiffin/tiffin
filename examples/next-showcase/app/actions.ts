"use server";

import { revalidatePath, updateTag } from "next/cache";
import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { sql } from "@/lib/db";

export async function addToCart() {
  const jar = await cookies();
  const n = (Number(jar.get("cart")?.value ?? 0) || 0) + 1;
  jar.set("cart", String(n), { path: "/", sameSite: "lax" });
}

// No real auth yet: the proxy only checks that this cookie is there.
export async function login() {
  (await cookies()).set("showcase_session", "demo", { path: "/", httpOnly: true, sameSite: "lax" });
  redirect("/dashboard");
}

export type NoteState = { error?: string; saved?: number };

export async function addNote(_prev: NoteState, form: FormData): Promise<NoteState> {
  const text = String(form.get("text") ?? "").trim().slice(0, 280);
  if (!text) return { error: "Write something first." };
  // A little work, so the optimistic row is visible before the real one.
  await new Promise((r) => setTimeout(r, 400));
  const [row] = await sql()<{ id: number }[]>`insert into notes (text) values (${text}) returning id`;
  revalidatePath("/dashboard");
  return { saved: row!.id };
}

// Called with a plain number, so the bench can post it as `[id]`.
export async function likeProduct(id: number): Promise<number> {
  const [row] = await sql()<{ likes: number }[]>`update products set likes = likes + 1 where id = ${id} returning likes`;
  return row?.likes ?? 0;
}

// Admin: edit a post and regenerate its page (ISR on demand).
export async function editPost(form: FormData) {
  const slug = String(form.get("slug") ?? "");
  await sql()`update posts set rev = rev + 1, title = regexp_replace(title, ' \\(r[0-9]+\\)$', '') || ' (r' || (rev + 1) || ')' where slug = ${slug}`;
  revalidatePath(`/blog/${slug}`);
}

// Admin: restock everything and refresh the cached product grid.
export async function restock() {
  await sql()`update products set stock = stock + 5`;
  updateTag("products");
}
