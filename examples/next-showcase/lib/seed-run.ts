// The release command (tiffin.config.ts): runs once per deploy, after the
// build and before the new instances start.
import { sql } from "./db";
import { seed } from "./seed";

const t = Date.now();
await seed();
await sql().end();
console.log(`[showcase] seed ok in ${Date.now() - t} ms`);
