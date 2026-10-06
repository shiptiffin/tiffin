import { cacheLife, cacheTag } from "next/cache";
import { POSTS, PRODUCTS, type Post, type Product } from "./content";
import { orSeed, sql } from "./db";

// Cached reads ("use cache"): shared by every instance through the box's
// cache handlers (Valkey), invalidated by tag or path from the dashboard.

export async function getProducts(): Promise<{ products: Product[]; at: string }> {
  "use cache";
  cacheTag("products");
  cacheLife("hours");
  const products = await orSeed(
    () => sql()<Product[]>`select id, slug, name, price, stock, likes from products order by id`,
    () => PRODUCTS,
  );
  return { products: [...products], at: new Date().toISOString() };
}

export async function getPost(slug: string): Promise<(Post & { at: string }) | null> {
  "use cache";
  cacheTag("post:" + slug);
  // ISR: the page is regenerated at most once a minute, or on revalidatePath.
  cacheLife({ stale: 60, revalidate: 60, expire: 3600 });
  const rows = await orSeed(
    () => sql()<Post[]>`select slug, title, body, rev from posts where slug = ${slug}`,
    () => POSTS.filter((p) => p.slug === slug),
  );
  return rows[0] ? { ...rows[0], at: new Date().toISOString() } : null;
}

export const postSlugs = () => POSTS.map((p) => p.slug);
