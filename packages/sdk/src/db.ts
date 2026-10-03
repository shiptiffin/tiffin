/**
 * `tiffin-sdk/db`: Firestore-style document collections on the project's
 * Postgres database (JSONB with a GIN index), for data that doesn't need a
 * schema yet.
 *
 * ```ts
 * import { db } from "tiffin-sdk/db";
 *
 * type Post = { title: string; author: string; tags?: string[]; views?: number };
 * const posts = db().collection<Post>("posts");
 *
 * const p = await posts.insert({ title: "Hello", author: "ada" });
 * await posts.update(p.id, { views: 1 });                  // shallow merge
 * const mine = await posts.find({ where: { author: "ada" }, orderBy: "createdAt", desc: true, limit: 20 });
 * const one = await posts.get(p.id);                       // null when missing
 * await posts.delete(p.id);
 * ```
 *
 * Documents live in one table, `tiffin_docs (collection, id, data jsonb,
 * created_at, updated_at)`, created on first use. `where` matches by JSON
 * containment (`data @> where`), which the GIN index serves, so nested
 * objects and array members work too: `{ tags: ["go"] }` matches documents
 * whose tags include "go".
 *
 * The connection defaults to `new Bun.SQL(process.env.DATABASE_URL)` (the
 * box sets DATABASE_URL for every app with a postgres service). Any client
 * with `unsafe(sql, params)` works: Bun's SQL or postgres.js.
 */

/** A SQL client: Bun's `SQL` or postgres.js both fit. */
export interface SqlClient {
  unsafe(query: string, params?: unknown[]): PromiseLike<unknown[]>;
}

/** A stored document: your fields plus id and timestamps. */
export type Doc<T> = T & { id: string; createdAt: string; updatedAt: string };

/** Document fields may be any JSON value. */
export type JsonObject = { [key: string]: unknown };

/** Options for find and count. */
export interface FindOptions<T> {
  /** Match documents containing these fields (JSON containment, uses the GIN index). */
  where?: Partial<T> | JsonObject;
  /** "createdAt" (default), "updatedAt", "id" or a top-level field of the document. */
  orderBy?: "createdAt" | "updatedAt" | "id" | (keyof T & string);
  /** Newest/largest first. */
  desc?: boolean;
  /** At most this many (default 100, max 1000). */
  limit?: number;
  offset?: number;
}

export const TABLE = "tiffin_docs";

const COLLECTION = /^[A-Za-z][A-Za-z0-9_.-]{0,62}$/;
const FIELD = /^[A-Za-z_][A-Za-z0-9_]{0,62}$/;

const SCHEMA = [
  `CREATE TABLE IF NOT EXISTS ${TABLE} (
    collection text NOT NULL,
    id text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection, id)
  )`,
  `CREATE INDEX IF NOT EXISTS ${TABLE}_data_gin ON ${TABLE} USING gin (data jsonb_path_ops)`,
  `CREATE INDEX IF NOT EXISTS ${TABLE}_created ON ${TABLE} (collection, created_at)`,
];

type Row = { id: string; data: unknown; created_at: unknown; updated_at: unknown };

function iso(v: unknown): string {
  return v instanceof Date ? v.toISOString() : String(v);
}

function toDoc<T>(r: Row): Doc<T> {
  const data = (typeof r.data === "string" ? JSON.parse(r.data) : r.data) as T;
  return { ...data, id: r.id, createdAt: iso(r.created_at), updatedAt: iso(r.updated_at) };
}

function strip(doc: object): JsonObject {
  const { id: _i, createdAt: _c, updatedAt: _u, ...rest } = doc as JsonObject;
  return rest;
}

function newId(): string {
  const b = globalThis.Bun as { randomUUIDv7?: () => string } | undefined;
  return b?.randomUUIDv7 ? b.randomUUIDv7() : crypto.randomUUID();
}

/** One collection of documents of type T. */
export class Collection<T extends object = JsonObject> {
  constructor(
    private readonly db: Database,
    readonly name: string,
  ) {
    if (!COLLECTION.test(name)) {
      throw new Error(`invalid collection name ${JSON.stringify(name)}: letters, digits, _ . - (start with a letter)`);
    }
  }

  /** Insert a document. Pass `id` to choose it; otherwise a time-ordered UUID is used. */
  async insert(doc: T & { id?: string }): Promise<Doc<T>> {
    const id = doc.id ?? newId();
    const rows = await this.db.query<Row>(
      `INSERT INTO ${TABLE} (collection, id, data) VALUES ($1, $2, $3::text::jsonb) RETURNING id, data, created_at, updated_at`,
      [this.name, id, JSON.stringify(strip(doc))],
    );
    return toDoc<T>(rows[0]!);
  }

  /** Insert many documents in one statement. */
  async insertMany(docs: (T & { id?: string })[]): Promise<Doc<T>[]> {
    if (docs.length === 0) return [];
    const payload = docs.map((d) => ({ id: d.id ?? newId(), data: strip(d) }));
    const rows = await this.db.query<Row>(
      `INSERT INTO ${TABLE} (collection, id, data)
       SELECT $1, d->>'id', d->'data' FROM jsonb_array_elements($2::text::jsonb) AS d
       RETURNING id, data, created_at, updated_at`,
      [this.name, JSON.stringify(payload)],
    );
    return rows.map((r) => toDoc<T>(r));
  }

  /** Get a document by id, or null. */
  async get(id: string): Promise<Doc<T> | null> {
    const rows = await this.db.query<Row>(
      `SELECT id, data, created_at, updated_at FROM ${TABLE} WHERE collection = $1 AND id = $2`,
      [this.name, id],
    );
    return rows[0] ? toDoc<T>(rows[0]) : null;
  }

  /** Find documents. */
  async find(opts: FindOptions<T> = {}): Promise<Doc<T>[]> {
    const params: unknown[] = [this.name];
    let sql = `SELECT id, data, created_at, updated_at FROM ${TABLE} WHERE collection = $1`;
    if (opts.where && Object.keys(opts.where).length > 0) {
      params.push(JSON.stringify(opts.where));
      sql += ` AND data @> $${params.length}::text::jsonb`;
    }
    sql += ` ORDER BY ${orderExpr(opts.orderBy)} ${opts.desc ? "DESC" : "ASC"}, id ${opts.desc ? "DESC" : "ASC"}`;
    const limit = Math.min(Math.max(Math.trunc(opts.limit ?? 100), 1), 1000);
    params.push(limit);
    sql += ` LIMIT $${params.length}`;
    if (opts.offset) {
      params.push(Math.max(Math.trunc(opts.offset), 0));
      sql += ` OFFSET $${params.length}`;
    }
    const rows = await this.db.query<Row>(sql, params);
    return rows.map((r) => toDoc<T>(r));
  }

  /** The first match, or null. */
  async findOne(where: Partial<T> | JsonObject): Promise<Doc<T> | null> {
    const [first] = await this.find({ where, limit: 1 });
    return first ?? null;
  }

  /** Count documents (optionally matching where). */
  async count(where?: Partial<T> | JsonObject): Promise<number> {
    const params: unknown[] = [this.name];
    let sql = `SELECT count(*)::int AS n FROM ${TABLE} WHERE collection = $1`;
    if (where && Object.keys(where).length > 0) {
      params.push(JSON.stringify(where));
      sql += ` AND data @> $2::text::jsonb`;
    }
    const rows = await this.db.query<{ n: number | string }>(sql, params);
    return Number(rows[0]?.n ?? 0);
  }

  /** Shallow-merge fields into a document. Returns it, or null when missing. */
  async update(id: string, patch: Partial<T>): Promise<Doc<T> | null> {
    const rows = await this.db.query<Row>(
      `UPDATE ${TABLE} SET data = data || $3::text::jsonb, updated_at = now()
       WHERE collection = $1 AND id = $2 RETURNING id, data, created_at, updated_at`,
      [this.name, id, JSON.stringify(strip(patch))],
    );
    return rows[0] ? toDoc<T>(rows[0]) : null;
  }

  /** Replace a document's fields entirely. Returns it, or null when missing. */
  async replace(id: string, doc: T): Promise<Doc<T> | null> {
    const rows = await this.db.query<Row>(
      `UPDATE ${TABLE} SET data = $3::text::jsonb, updated_at = now()
       WHERE collection = $1 AND id = $2 RETURNING id, data, created_at, updated_at`,
      [this.name, id, JSON.stringify(strip(doc))],
    );
    return rows[0] ? toDoc<T>(rows[0]) : null;
  }

  /** Delete a document. Returns whether it existed. */
  async delete(id: string): Promise<boolean> {
    const rows = await this.db.query<{ id: string }>(
      `DELETE FROM ${TABLE} WHERE collection = $1 AND id = $2 RETURNING id`,
      [this.name, id],
    );
    return rows.length > 0;
  }

  /** Delete every matching document (all of the collection without where). Returns how many. */
  async deleteMany(where?: Partial<T> | JsonObject): Promise<number> {
    const params: unknown[] = [this.name];
    let sql = `DELETE FROM ${TABLE} WHERE collection = $1`;
    if (where && Object.keys(where).length > 0) {
      params.push(JSON.stringify(where));
      sql += ` AND data @> $2::text::jsonb`;
    }
    const rows = await this.db.query<{ id: string }>(sql + " RETURNING id", params);
    return rows.length;
  }
}

function orderExpr(field: string | undefined): string {
  switch (field ?? "createdAt") {
    case "createdAt":
      return "created_at";
    case "updatedAt":
      return "updated_at";
    case "id":
      return "id";
  }
  if (!FIELD.test(field!)) throw new Error(`invalid orderBy field ${JSON.stringify(field)}`);
  return `data->'${field}'`;
}

/** A documents database on one SQL connection. */
export class Database {
  private ready: Promise<void> | null = null;

  constructor(readonly sql: SqlClient) {}

  /** A typed collection. */
  collection<T extends object = JsonObject>(name: string): Collection<T> {
    return new Collection<T>(this, name);
  }

  /** Creates the documents table and indexes (idempotent; runs once per Database). */
  ensureSchema(): Promise<void> {
    this.ready ??= (async () => {
      for (const stmt of SCHEMA) await this.sql.unsafe(stmt);
    })().catch((err) => {
      this.ready = null;
      throw err;
    });
    return this.ready;
  }

  /** Run a statement after making sure the schema exists. */
  async query<R>(query: string, params: unknown[] = []): Promise<R[]> {
    await this.ensureSchema();
    return (await this.sql.unsafe(query, params)) as R[];
  }
}

let shared: Database | null = null;

/**
 * The project's documents database. Without arguments it connects with
 * `new Bun.SQL(process.env.DATABASE_URL)` once and reuses it.
 */
export function db(client?: SqlClient): Database {
  if (client) return new Database(client);
  if (!shared) {
    const url = process.env.DATABASE_URL;
    if (!url) throw new Error("DATABASE_URL is not set: add services.postgres to tiffin.config.ts (the box sets it for your apps)");
    const B = globalThis.Bun as { SQL?: new (url: string) => SqlClient } | undefined;
    if (!B?.SQL) throw new Error("tiffin-sdk/db needs Bun's SQL; on another runtime pass a client: db(postgres(url))");
    shared = new Database(new B.SQL(url));
  }
  return shared;
}
