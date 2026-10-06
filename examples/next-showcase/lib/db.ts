import postgres from "postgres";

// postgres.js runs on Bun and on Node.js, so the same app can be measured on
// both. Tiffin sets DATABASE_URL (for the build: a read-only user).
const g = globalThis as { __showcaseSql?: postgres.Sql };

export function sql(): postgres.Sql {
  const url = process.env.DATABASE_URL;
  if (!url) throw new Error("DATABASE_URL is not set");
  g.__showcaseSql ??= postgres(url, { max: 10, idle_timeout: 30, connect_timeout: 5 });
  return g.__showcaseSql;
}

const building = () => process.env.NEXT_PHASE === "phase-production-build";

/**
 * Runs a query. The release command creates the tables after the build, so
 * the very first build finds none: it prerenders the seed rows instead (the
 * rows the release then writes). So does a build without DATABASE_URL.
 */
export async function orSeed<T>(query: () => Promise<T>, seed: () => T): Promise<T> {
  if (building() && !process.env.DATABASE_URL) return seed();
  try {
    return await query();
  } catch (err) {
    if (!building() || (err as { code?: string }).code !== "42P01") throw err;
    console.warn("[showcase] build: no tables yet (first deploy), prerendering the seed rows");
    return seed();
  }
}
