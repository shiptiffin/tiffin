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
function iso(v) {
    return v instanceof Date ? v.toISOString() : String(v);
}
function toDoc(r) {
    const data = (typeof r.data === "string" ? JSON.parse(r.data) : r.data);
    return { ...data, id: r.id, createdAt: iso(r.created_at), updatedAt: iso(r.updated_at) };
}
function strip(doc) {
    const { id: _i, createdAt: _c, updatedAt: _u, ...rest } = doc;
    return rest;
}
function newId() {
    const b = globalThis.Bun;
    return b?.randomUUIDv7 ? b.randomUUIDv7() : crypto.randomUUID();
}
/** One collection of documents of type T. */
export class Collection {
    db;
    name;
    constructor(db, name) {
        this.db = db;
        this.name = name;
        if (!COLLECTION.test(name)) {
            throw new Error(`invalid collection name ${JSON.stringify(name)}: letters, digits, _ . - (start with a letter)`);
        }
    }
    /** Insert a document. Pass `id` to choose it; otherwise a time-ordered UUID is used. */
    async insert(doc) {
        const id = doc.id ?? newId();
        const rows = await this.db.query(`INSERT INTO ${TABLE} (collection, id, data) VALUES ($1, $2, $3::text::jsonb) RETURNING id, data, created_at, updated_at`, [this.name, id, JSON.stringify(strip(doc))]);
        return toDoc(rows[0]);
    }
    /** Insert many documents in one statement. */
    async insertMany(docs) {
        if (docs.length === 0)
            return [];
        const payload = docs.map((d) => ({ id: d.id ?? newId(), data: strip(d) }));
        const rows = await this.db.query(`INSERT INTO ${TABLE} (collection, id, data)
       SELECT $1, d->>'id', d->'data' FROM jsonb_array_elements($2::text::jsonb) AS d
       RETURNING id, data, created_at, updated_at`, [this.name, JSON.stringify(payload)]);
        return rows.map((r) => toDoc(r));
    }
    /** Get a document by id, or null. */
    async get(id) {
        const rows = await this.db.query(`SELECT id, data, created_at, updated_at FROM ${TABLE} WHERE collection = $1 AND id = $2`, [this.name, id]);
        return rows[0] ? toDoc(rows[0]) : null;
    }
    /** Find documents. */
    async find(opts = {}) {
        const params = [this.name];
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
        const rows = await this.db.query(sql, params);
        return rows.map((r) => toDoc(r));
    }
    /** The first match, or null. */
    async findOne(where) {
        const [first] = await this.find({ where, limit: 1 });
        return first ?? null;
    }
    /** Count documents (optionally matching where). */
    async count(where) {
        const params = [this.name];
        let sql = `SELECT count(*)::int AS n FROM ${TABLE} WHERE collection = $1`;
        if (where && Object.keys(where).length > 0) {
            params.push(JSON.stringify(where));
            sql += ` AND data @> $2::text::jsonb`;
        }
        const rows = await this.db.query(sql, params);
        return Number(rows[0]?.n ?? 0);
    }
    /** Shallow-merge fields into a document. Returns it, or null when missing. */
    async update(id, patch) {
        const rows = await this.db.query(`UPDATE ${TABLE} SET data = data || $3::text::jsonb, updated_at = now()
       WHERE collection = $1 AND id = $2 RETURNING id, data, created_at, updated_at`, [this.name, id, JSON.stringify(strip(patch))]);
        return rows[0] ? toDoc(rows[0]) : null;
    }
    /** Replace a document's fields entirely. Returns it, or null when missing. */
    async replace(id, doc) {
        const rows = await this.db.query(`UPDATE ${TABLE} SET data = $3::text::jsonb, updated_at = now()
       WHERE collection = $1 AND id = $2 RETURNING id, data, created_at, updated_at`, [this.name, id, JSON.stringify(strip(doc))]);
        return rows[0] ? toDoc(rows[0]) : null;
    }
    /** Delete a document. Returns whether it existed. */
    async delete(id) {
        const rows = await this.db.query(`DELETE FROM ${TABLE} WHERE collection = $1 AND id = $2 RETURNING id`, [this.name, id]);
        return rows.length > 0;
    }
    /** Delete every matching document (all of the collection without where). Returns how many. */
    async deleteMany(where) {
        const params = [this.name];
        let sql = `DELETE FROM ${TABLE} WHERE collection = $1`;
        if (where && Object.keys(where).length > 0) {
            params.push(JSON.stringify(where));
            sql += ` AND data @> $2::text::jsonb`;
        }
        const rows = await this.db.query(sql + " RETURNING id", params);
        return rows.length;
    }
}
function orderExpr(field) {
    switch (field ?? "createdAt") {
        case "createdAt":
            return "created_at";
        case "updatedAt":
            return "updated_at";
        case "id":
            return "id";
    }
    if (!FIELD.test(field))
        throw new Error(`invalid orderBy field ${JSON.stringify(field)}`);
    return `data->'${field}'`;
}
/** A documents database on one SQL connection. */
export class Database {
    sql;
    ready = null;
    constructor(sql) {
        this.sql = sql;
    }
    /** A typed collection. */
    collection(name) {
        return new Collection(this, name);
    }
    /** Creates the documents table and indexes (idempotent; runs once per Database). */
    ensureSchema() {
        this.ready ??= (async () => {
            for (const stmt of SCHEMA)
                await this.sql.unsafe(stmt);
        })().catch((err) => {
            this.ready = null;
            throw err;
        });
        return this.ready;
    }
    /** Run a statement after making sure the schema exists. */
    async query(query, params = []) {
        await this.ensureSchema();
        return (await this.sql.unsafe(query, params));
    }
}
let shared = null;
/**
 * The project's documents database. Without arguments it connects with
 * `new Bun.SQL(process.env.DATABASE_URL)` once and reuses it.
 */
export function db(client) {
    if (client)
        return new Database(client);
    if (!shared) {
        const url = process.env.DATABASE_URL;
        if (!url)
            throw new Error("DATABASE_URL is not set: add services.postgres to tiffin.config.ts (the box sets it for your apps)");
        const B = globalThis.Bun;
        if (!B?.SQL)
            throw new Error("tiffin-sdk/db needs Bun's SQL; on another runtime pass a client: db(postgres(url))");
        shared = new Database(new B.SQL(url));
    }
    return shared;
}
