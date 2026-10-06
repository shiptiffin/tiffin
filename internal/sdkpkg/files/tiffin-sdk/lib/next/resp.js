/**
 * A small Redis/Valkey client over a socket (RESP2), for Bun and Node alike:
 * binary-safe (bulk replies are Buffers, arguments may be bytes), pipelined
 * (commands issued in the same tick go out in one write), no dependencies.
 * Just enough for tiffin-sdk/next's cache handlers.
 *
 * URLs: redis://[user:pass@]host[:port][/db], rediss://… (TLS),
 * redis+unix://[user:pass@]/path/to.sock (also unix://).
 */
import { connect as netConnect } from "node:net";
import { connect as tlsConnect } from "node:tls";
export class ReplyError extends Error {
}
const CRLF = Buffer.from("\r\n");
export class RespClient {
    url;
    timeoutMs;
    /** Bulk replies are Buffers and arguments may be bytes. */
    binary = true;
    sock;
    ready = false;
    out = [];
    flushQueued = false;
    waiters = [];
    chunks = [];
    have = 0;
    need = 0;
    downUntil = 0;
    fails = 0;
    timer;
    constructor(url, 
    /** A command with no reply after this long fails, and the connection is reset. */
    timeoutMs = 2000) {
        this.url = url;
        this.timeoutMs = timeoutMs;
    }
    send(cmd, args = []) {
        return this.pipeline([[cmd, ...args]]).then((r) => {
            const v = r[0];
            if (v instanceof ReplyError)
                throw v;
            return v;
        });
    }
    /** Sends commands together; replies come back in order (errors as ReplyError values). */
    pipeline(cmds) {
        if (Date.now() < this.downUntil)
            return Promise.reject(new Error("valkey unavailable (retrying shortly)"));
        if (!this.sock)
            this.open();
        return Promise.all(cmds.map((c) => this.enqueue(c)));
    }
    close() {
        this.reset(new Error("client closed"), false);
    }
    enqueue(cmd) {
        return new Promise((resolve, reject) => {
            // The socket keeps the process alive only while a reply is due, so a
            // script that is done exits without close().
            if (this.waiters.length === 0)
                this.sock?.ref();
            this.waiters.push({ resolve, reject });
            this.out.push(encode(cmd));
            if (!this.timer)
                this.arm();
            if (this.ready && !this.flushQueued) {
                this.flushQueued = true;
                queueMicrotask(() => this.flush());
            }
        });
    }
    flush() {
        this.flushQueued = false;
        if (!this.sock || !this.ready || this.out.length === 0)
            return;
        const buf = this.out.length === 1 ? this.out[0] : Buffer.concat(this.out);
        this.out = [];
        this.sock.write(buf);
    }
    open() {
        const t = target(this.url);
        const sock = t.path
            ? netConnect({ path: t.path })
            : t.tls
                ? tlsConnect({ host: t.host, port: t.port, servername: t.host })
                : netConnect({ host: t.host, port: t.port });
        sock.setNoDelay?.(true);
        this.sock = sock;
        // Handshake commands go first; their replies are checked, not returned.
        const hello = [];
        if (t.pass)
            hello.push(t.user ? ["AUTH", t.user, t.pass] : ["AUTH", t.pass]);
        if (t.db && t.db !== "0")
            hello.push(["SELECT", t.db]);
        for (const c of hello) {
            this.waiters.push({
                resolve: (v) => void (v instanceof ReplyError && this.reset(new Error(`valkey ${c[0]}: ${v.message}`), true)),
                reject: () => { },
            });
            this.out.push(encode(c));
        }
        sock.once(t.tls ? "secureConnect" : "connect", () => {
            if (this.sock !== sock)
                return;
            this.ready = true;
            this.fails = 0;
            this.flush();
        });
        sock.on("data", (d) => {
            if (this.sock !== sock)
                return;
            try {
                this.onData(d);
            }
            catch (e) {
                this.reset(e, true);
            }
        });
        sock.on("error", (e) => this.sock === sock && this.reset(e, true));
        sock.on("close", () => this.sock === sock && this.reset(new Error("valkey connection closed"), false));
    }
    arm() {
        this.timer = setTimeout(() => {
            this.timer = undefined;
            if (this.waiters.length > 0)
                this.reset(new Error(`valkey did not answer within ${this.timeoutMs} ms`), true);
        }, this.timeoutMs);
        this.timer.unref?.();
    }
    reset(err, down) {
        const sock = this.sock;
        this.sock = undefined;
        this.ready = false;
        this.out = [];
        this.chunks = [];
        this.have = this.need = 0;
        if (this.timer)
            clearTimeout(this.timer);
        this.timer = undefined;
        // After a failure, fail fast for a moment instead of queueing behind a
        // dead server: 0.2 s, doubling to 5 s while it stays down.
        if (down)
            this.downUntil = Date.now() + Math.min(5000, 100 * 2 ** ++this.fails);
        const ws = this.waiters;
        this.waiters = [];
        for (const w of ws)
            w.reject(err);
        sock?.destroy();
    }
    onData(d) {
        this.chunks.push(d);
        this.have += d.length;
        if (this.have < this.need)
            return; // a large reply still arriving
        let buf = this.chunks.length === 1 ? this.chunks[0] : Buffer.concat(this.chunks, this.have);
        let off = 0;
        while (off < buf.length) {
            const r = parse(buf, off);
            if (!r)
                break;
            off = r.end;
            const w = this.waiters.shift();
            w?.resolve(r.value);
        }
        if (off >= buf.length) {
            this.chunks = [];
            this.have = this.need = 0;
        }
        else {
            buf = buf.subarray(off);
            this.chunks = [buf];
            this.have = buf.length;
            this.need = needed(buf);
        }
        if (this.timer)
            clearTimeout(this.timer);
        this.timer = undefined;
        if (this.waiters.length > 0)
            this.arm();
        else
            this.sock?.unref();
    }
}
/** Parses a connection URL. redis+unix://user:pass@/path is not a WHATWG URL, so unix ones are split by hand. */
export function target(url) {
    const m = /^(?:redis\+unix|unix):\/\/(?:(.*)@)?(\/[^?]*)(?:\?(.*))?$/.exec(url);
    if (m) {
        const info = m[1] ?? "";
        const i = info.indexOf(":");
        const user = decodeURIComponent(i < 0 ? info : info.slice(0, i));
        const pass = i < 0 ? "" : decodeURIComponent(info.slice(i + 1));
        return { host: "", port: 0, tls: false, path: m[2], user, pass, db: new URLSearchParams(m[3] ?? "").get("db") };
    }
    const u = new URL(url);
    return {
        host: u.hostname || "127.0.0.1",
        port: Number(u.port || 6379),
        tls: u.protocol === "rediss:",
        user: decodeURIComponent(u.username),
        pass: decodeURIComponent(u.password),
        db: u.pathname.slice(1),
    };
}
function encode(cmd) {
    const parts = [Buffer.from(`*${cmd.length}\r\n`)];
    for (const a of cmd) {
        const b = typeof a === "string" ? Buffer.from(a) : Buffer.from(a.buffer, a.byteOffset, a.byteLength);
        parts.push(Buffer.from(`$${b.length}\r\n`), b, CRLF);
    }
    return Buffer.concat(parts);
}
function line(buf, off) {
    const i = buf.indexOf(13, off);
    return i < 0 || i + 1 >= buf.length ? -1 : i;
}
/** One reply starting at off, or undefined when it is not complete yet. */
function parse(buf, off) {
    const eol = line(buf, off + 1);
    if (eol < 0)
        return undefined;
    const type = buf[off];
    const head = buf.toString("latin1", off + 1, eol);
    const next = eol + 2;
    switch (type) {
        case 43: // +
            return { value: head, end: next };
        case 45: // -
            return { value: new ReplyError(buf.toString("utf8", off + 1, eol)), end: next };
        case 58: // :
            return { value: Number(head), end: next };
        case 36: {
            // $
            const n = Number(head);
            if (n < 0)
                return { value: null, end: next };
            if (next + n + 2 > buf.length)
                return undefined;
            return { value: Buffer.from(buf.subarray(next, next + n)), end: next + n + 2 };
        }
        case 42: {
            // *
            const n = Number(head);
            if (n < 0)
                return { value: null, end: next };
            const out = [];
            let at = next;
            for (let i = 0; i < n; i++) {
                const r = parse(buf, at);
                if (!r)
                    return undefined;
                out.push(r.value);
                at = r.end;
            }
            return { value: out, end: at };
        }
        default:
            throw new Error(`valkey: unexpected reply type ${String.fromCharCode(type ?? 0)}`);
    }
}
/** How many bytes the first (incomplete) reply needs, when it is a known-size bulk string. */
function needed(buf) {
    if (buf[0] !== 36)
        return 0;
    const eol = line(buf, 1);
    if (eol < 0)
        return 0;
    return eol + 2 + Number(buf.toString("latin1", 1, eol)) + 2;
}
