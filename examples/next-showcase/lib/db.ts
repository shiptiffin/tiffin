import postgres from "postgres";

// postgres.js runs on Bun and on Node.js, so the same app can be measured on
// both. Tiffin sets DATABASE_URL.
const g = globalThis as { __showcaseSql?: postgres.Sql };

export function sql(): postgres.Sql {
  const url = process.env.DATABASE_URL;
  if (!url) throw new Error("DATABASE_URL is not set");
  g.__showcaseSql ??= postgres(url, { max: 10, idle_timeout: 30, connect_timeout: 5 });
  return g.__showcaseSql;
}

const building = () => process.env.NEXT_PHASE === "phase-production-build";

/**
 * Runs a query; while `next build` prerenders without a reachable database
 * it returns the seed data instead (the page is rendered again from the
 * database when its cache expires or is revalidated).
 */
export async function orSeed<T>(query: () => Promise<T>, seed: () => T): Promise<T> {
  if (building() && !process.env.DATABASE_URL) {
    console.warn("[showcase] build: no DATABASE_URL, prerendering seed data");
    return seed();
  }
  try {
    return await query();
  } catch (err) {
    if (!building()) throw err;
    console.warn(`[showcase] build: database not reachable (${(err as Error).message}), prerendering seed data`);
    return seed();
  }
}
