import { RespClient } from "../resp.js";
/** A Valkey error, with its code ("NOPERM", "OOM", "WRONGTYPE"...) and a message that says what to do. */
export declare class KVError extends Error {
    /** The first word of the server's error, or "CONNECTION" / "TIMEOUT". */
    readonly code: string;
    name: string;
    constructor(message: string, 
    /** The first word of the server's error, or "CONNECTION" / "TIMEOUT". */
    code: string);
}
export interface Conn {
    send(cmd: string, args: string[]): Promise<unknown>;
    /** Sends commands in one write; replies in order, errors as KVError values. */
    batch(cmds: string[][]): Promise<unknown[]>;
    /** The client underneath (Bun's RedisClient or the RESP client). */
    readonly raw: unknown;
    close(): void;
}
/** What a Bun.RedisClient (or any client passed in) must offer. */
export interface RedisLike {
    send(command: string, args: string[]): Promise<unknown>;
    close?(): void;
}
/** Turns a server or connection error into a KVError with a plain message. */
export declare function kvError(e: unknown, cmd?: string): KVError;
/** The SDK's RESP client: works on Node, Bun and anything with node:net. */
export declare class RespConn implements Conn {
    readonly raw: RespClient;
    constructor(url: string, timeoutMs: number);
    send(cmd: string, args: string[]): Promise<unknown>;
    batch(cmds: string[][]): Promise<unknown[]>;
    close(): void;
}
type BunRedisCtor = new (url: string, opts?: Record<string, unknown>) => RedisLike & {
    connected?: boolean;
};
/**
 * Bun's RedisClient, with what it lacks for a long-running app: a command
 * timeout (it otherwise queues while reconnecting, ~30 s), and a fresh client
 * after it gives up reconnecting (it then fails every call for good), with
 * backoff so a dead server is not hammered.
 */
export declare class BunConn implements Conn {
    private readonly url;
    private readonly timeoutMs;
    private readonly Ctor;
    private c;
    private fails;
    private downUntil;
    constructor(url: string, timeoutMs: number, Ctor: BunRedisCtor);
    get raw(): unknown;
    private client;
    send(cmd: string, args: string[]): Promise<unknown>;
    batch(cmds: string[][]): Promise<unknown[]>;
    private drop;
    close(): void;
}
/** A client you pass in (Bun.redis, or anything with send(command, args)). */
export declare class ClientConn implements Conn {
    readonly raw: RedisLike;
    constructor(raw: RedisLike);
    send(cmd: string, args: string[]): Promise<unknown>;
    batch(cmds: string[][]): Promise<unknown[]>;
    close(): void;
}
/** Bun's RedisClient constructor when running on Bun. */
export declare function bunRedis(): BunRedisCtor | undefined;
export {};
