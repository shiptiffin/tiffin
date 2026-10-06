// The connection under @shiptiffin/sdk/kv: Bun's built-in RedisClient on Bun, the
// SDK's own RESP client (no dependencies) elsewhere. Both pipeline commands
// issued in the same tick into one write, so Promise.all over several calls
// is one round trip. Replies are normalised to strings, numbers, arrays and
// (from Bun, which speaks RESP3) plain objects; the commands layer accepts both.
import { ReplyError, RespClient } from "../resp.js";
/** A Valkey error, with its code ("NOPERM", "OOM", "WRONGTYPE"...) and a message that says what to do. */
export class KVError extends Error {
    code;
    name = "KVError";
    constructor(message, 
    /** The first word of the server's error, or "CONNECTION" / "TIMEOUT". */
    code) {
        super(message);
        this.code = code;
    }
}
// Commands a project's Valkey user never may run on Tiffin (they reach past
// its prefix or the server). Any other refused command is a write refused
// because the project is over its memory limit (or a build's read-only user).
const NEVER = new Set("SCAN KEYS RANDOMKEY SELECT MOVE SWAPDB FLUSHDB FLUSHALL CONFIG DEBUG SHUTDOWN MONITOR ACL SAVE BGSAVE BGREWRITEAOF REPLICAOF SLAVEOF FAILOVER CLUSTER MIGRATE SORT SORT_RO".split(" "));
/** Turns a server or connection error into a KVError with a plain message. */
export function kvError(e, cmd = "") {
    if (e instanceof KVError)
        return e;
    const msg = e instanceof Error ? e.message : String(e);
    let code = /^[A-Z]+\b/.exec(msg)?.[0] ?? "ERR";
    let c = cmd.toUpperCase();
    // Inside a Lua script: "ERR ACL failure in script: ... to run the 'hset' command".
    const inner = /ACL failure in script: .*to run the '([\w|]+)'/.exec(msg);
    if (inner)
        [code, c] = ["NOPERM", inner[1].toUpperCase()];
    if (code === "OOM")
        return new KVError(`KV is full: the box's Valkey is over its memory limit, so ${c} was refused. Delete keys or give them an expiry. (${msg})`, code);
    if (code === "NOPERM") {
        if (/to run the '/.test(msg) && NEVER.has(c))
            return new KVError(`KV: ${c} is not available to apps on Tiffin (one Valkey is shared by every project). (${msg})`, code);
        if (/to run the '/.test(msg))
            return new KVError(`KV is over this project's memory limit, so writes are refused until it is back under it (reads and deletes still work). Delete keys, give them an expiry, or raise services.valkey.maxMemoryMB. During a build, KV is read-only. (${msg})`, code);
        return new KVError(`KV: that key or channel is outside this project's prefix. Pass plain names; the SDK adds VALKEY_PREFIX. (${msg})`, code);
    }
    if (code === "WRONGPASS" || code === "NOAUTH")
        return new KVError(`KV: Valkey refused the user and password in REDIS_URL. Is it this project's current URL? (${msg})`, code);
    if (code === "WRONGTYPE")
        return new KVError(`KV: ${c} on a key that holds another type (${msg})`, code);
    if (/ECONNREFUSED|ENOENT|closed|failed|unavailable|reconnect|socket|EPIPE|ECONNRESET/i.test(msg) && !/^[A-Z]+ /.test(msg))
        return new KVError(`KV: cannot reach Valkey (${msg}). It retries on the next call.`, "CONNECTION");
    if (/did not answer within/.test(msg))
        return new KVError(`KV: ${msg}`, "TIMEOUT");
    return new KVError(msg, code);
}
/** Buffers to strings, RESP2 errors to KVError values. */
function norm(v) {
    if (v instanceof Uint8Array)
        return Buffer.from(v.buffer, v.byteOffset, v.byteLength).toString("utf8");
    if (v instanceof ReplyError)
        return kvError(v);
    if (Array.isArray(v))
        return v.map(norm);
    return v;
}
/** The SDK's RESP client: works on Node, Bun and anything with node:net. */
export class RespConn {
    raw;
    constructor(url, timeoutMs) {
        this.raw = new RespClient(url, timeoutMs);
    }
    async send(cmd, args) {
        try {
            return norm(await this.raw.send(cmd, args));
        }
        catch (e) {
            throw kvError(e, cmd);
        }
    }
    async batch(cmds) {
        try {
            const out = norm(await this.raw.pipeline(cmds));
            return out.map((v, i) => (v instanceof KVError ? kvError(new Error(v.message), cmds[i][0]) : v));
        }
        catch (e) {
            throw kvError(e, cmds[0]?.[0]);
        }
    }
    close() {
        this.raw.close();
    }
}
/**
 * Bun's RedisClient, with what it lacks for a long-running app: a command
 * timeout (it otherwise queues while reconnecting, ~30 s), and a fresh client
 * after it gives up reconnecting (it then fails every call for good), with
 * backoff so a dead server is not hammered.
 */
export class BunConn {
    url;
    timeoutMs;
    Ctor;
    c;
    fails = 0;
    downUntil = 0;
    constructor(url, timeoutMs, Ctor) {
        this.url = url;
        this.timeoutMs = timeoutMs;
        this.Ctor = Ctor;
    }
    get raw() {
        return this.client();
    }
    client() {
        if (!this.c)
            this.c = new this.Ctor(this.url, { connectionTimeout: this.timeoutMs, maxRetries: 3, enableAutoPipelining: true });
        return this.c;
    }
    send(cmd, args) {
        if (Date.now() < this.downUntil)
            return Promise.reject(new KVError("KV: cannot reach Valkey (retrying shortly)", "CONNECTION"));
        let c;
        try {
            c = this.client();
        }
        catch (e) {
            return Promise.reject(kvError(e, cmd));
        }
        let timer;
        const timeout = new Promise((_, reject) => {
            timer = setTimeout(() => reject(new KVError(`KV: Valkey did not answer ${cmd} within ${this.timeoutMs} ms`, "TIMEOUT")), this.timeoutMs);
        });
        return Promise.race([c.send(cmd, args), timeout]).then((v) => {
            clearTimeout(timer);
            this.fails = 0;
            return v;
        }, (e) => {
            clearTimeout(timer);
            const err = kvError(e, cmd);
            if (err.code === "CONNECTION" || err.code === "TIMEOUT")
                this.drop(c);
            throw err;
        });
    }
    batch(cmds) {
        // Issued in one synchronous loop, so they go out together and in order
        // (MULTI ... EXEC stays contiguous on the connection).
        return Promise.all(cmds.map(([cmd, ...args]) => this.send(cmd, args).catch((e) => kvError(e, cmd))));
    }
    drop(c) {
        if (this.c !== c)
            return;
        if (c.connected)
            return; // a slow command, not a dead connection
        this.c = undefined;
        try {
            c.close?.();
        }
        catch {
            // already closed
        }
        this.fails++;
        this.downUntil = Date.now() + Math.min(5000, 100 * 2 ** this.fails);
    }
    close() {
        this.c?.close?.();
        this.c = undefined;
    }
}
/** A client you pass in (Bun.redis, or anything with send(command, args)). */
export class ClientConn {
    raw;
    constructor(raw) {
        this.raw = raw;
    }
    async send(cmd, args) {
        try {
            return norm(await this.raw.send(cmd, args));
        }
        catch (e) {
            throw kvError(e, cmd);
        }
    }
    batch(cmds) {
        return Promise.all(cmds.map(([cmd, ...args]) => this.send(cmd, args).catch((e) => kvError(e, cmd))));
    }
    close() {
        this.raw.close?.();
    }
}
/** Bun's RedisClient constructor when running on Bun. */
export function bunRedis() {
    return globalThis.Bun?.RedisClient;
}
