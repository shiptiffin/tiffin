// The part of Bun's built-in SQLite that seed-analytics.ts uses (the
// dashboard doesn't depend on Bun's own types).
declare module "bun:sqlite" {
  export class Database {
    constructor(path: string);
    exec(sql: string): void;
    prepare(sql: string): { run(...params: unknown[]): void };
    query(sql: string): { all(...params: unknown[]): unknown[] };
    close(): void;
  }
}
