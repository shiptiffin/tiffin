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
export type Doc<T> = T & {
    id: string;
    createdAt: string;
    updatedAt: string;
};
/** Document fields may be any JSON value. */
export type JsonObject = {
    [key: string]: unknown;
};
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
export declare const TABLE = "tiffin_docs";
/** One collection of documents of type T. */
export declare class Collection<T extends object = JsonObject> {
    private readonly db;
    readonly name: string;
    constructor(db: Database, name: string);
    /** Insert a document. Pass `id` to choose it; otherwise a time-ordered UUID is used. */
    insert(doc: T & {
        id?: string;
    }): Promise<Doc<T>>;
    /** Insert many documents in one statement. */
    insertMany(docs: (T & {
        id?: string;
    })[]): Promise<Doc<T>[]>;
    /** Get a document by id, or null. */
    get(id: string): Promise<Doc<T> | null>;
    /** Find documents. */
    find(opts?: FindOptions<T>): Promise<Doc<T>[]>;
    /** The first match, or null. */
    findOne(where: Partial<T> | JsonObject): Promise<Doc<T> | null>;
    /** Count documents (optionally matching where). */
    count(where?: Partial<T> | JsonObject): Promise<number>;
    /** Shallow-merge fields into a document. Returns it, or null when missing. */
    update(id: string, patch: Partial<T>): Promise<Doc<T> | null>;
    /** Replace a document's fields entirely. Returns it, or null when missing. */
    replace(id: string, doc: T): Promise<Doc<T> | null>;
    /** Delete a document. Returns whether it existed. */
    delete(id: string): Promise<boolean>;
    /** Delete every matching document (all of the collection without where). Returns how many. */
    deleteMany(where?: Partial<T> | JsonObject): Promise<number>;
}
/** A documents database on one SQL connection. */
export declare class Database {
    readonly sql: SqlClient;
    private ready;
    constructor(sql: SqlClient);
    /** A typed collection. */
    collection<T extends object = JsonObject>(name: string): Collection<T>;
    /** Creates the documents table and indexes (idempotent; runs once per Database). */
    ensureSchema(): Promise<void>;
    /** Run a statement after making sure the schema exists. */
    query<R>(query: string, params?: unknown[]): Promise<R[]>;
}
/**
 * The project's documents database. Without arguments it connects with
 * `new Bun.SQL(process.env.DATABASE_URL)` once and reuses it.
 */
export declare function db(client?: SqlClient): Database;
