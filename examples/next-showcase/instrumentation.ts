// Runs once per server process before it takes requests: seeds the database
// and the bucket on the first start (idempotent after that).
export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs") return;
  if (process.env.NEXT_PHASE === "phase-production-build" || !process.env.DATABASE_URL) return;
  const { seed } = await import("./lib/seed");
  const t = Date.now();
  try {
    await seed();
    console.log(`[showcase] seed ok in ${Date.now() - t} ms`);
  } catch (err) {
    console.error("[showcase] seed failed:", err);
  }
}
