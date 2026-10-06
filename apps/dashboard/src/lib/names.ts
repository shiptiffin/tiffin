/**
 * What the parts of a project are called, everywhere in the interface: the
 * nav, tiles, Add, History sentences, ⌘K. The technology is only ever the
 * small subtitle (and in connection strings and Advanced).
 *
 *   PARTS.postgres.name  "Database"     PARTS.postgres.sub  "Postgres 18"
 *   PARTS.postgres.a     "a database"   (for sentences: "Added a database to shop")
 */
export type PartKey = "postgres" | "valkey" | "storage" | "email" | "auth" | "jobs" | "analytics" | "health" | "shield";

export const PARTS: Record<PartKey, { name: string; sub: string; a: string }> = {
  postgres: { name: "Database", sub: "Postgres 18", a: "a database" },
  valkey: { name: "KV", sub: "Redis-compatible · also a cache", a: "a KV store" },
  storage: { name: "Files", sub: "S3-compatible", a: "files" },
  email: { name: "Email", sub: "Send and catch email", a: "email" },
  auth: { name: "Auth", sub: "Users, passkeys, Google, GitHub", a: "auth" },
  jobs: { name: "Jobs", sub: "Background work, schedules, workflows", a: "jobs" },
  analytics: { name: "Analytics", sub: "Cookieless", a: "analytics" },
  health: { name: "Health", sub: "Logs, errors, alerts", a: "health" },
  shield: { name: "Shield", sub: "Bots, rate limits, firewall", a: "the shield" },
};

/** A service key from the manifest ("queue" is Jobs) to its name; unknown keys stay as they are. */
export const partName = (key: string) => PARTS[(key === "queue" ? "jobs" : key) as PartKey]?.name ?? key;
export const partSub = (key: string) => PARTS[(key === "queue" ? "jobs" : key) as PartKey]?.sub ?? "";
/** "a database", for sentences. */
export const partA = (key: string) => PARTS[(key === "queue" ? "jobs" : key) as PartKey]?.a ?? key;
/** "the database", for sentences about one that exists. */
export const partThe = (key: string) => {
  const a = partA(key);
  return a.startsWith("a ") ? `the ${a.slice(2)}` : a;
};

/** The order parts are shown in. */
export const PART_ORDER = ["postgres", "storage", "auth", "email", "analytics", "valkey"] as const;
