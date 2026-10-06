export type Arg = string | Uint8Array;
export type Reply = null | number | string | Buffer | ReplyError | Reply[];
export declare class ReplyError extends Error {
}
export declare class RespClient {
    readonly url: string;
    /** A command with no reply after this long fails, and the connection is reset. */
    readonly timeoutMs: number;
    /** Bulk replies are Buffers and arguments may be bytes. */
    readonly binary = true;
    private sock;
    private ready;
    private out;
    private flushQueued;
    private waiters;
    private chunks;
    private have;
    private need;
    private downUntil;
    private fails;
    private timer;
    constructor(url: string, 
    /** A command with no reply after this long fails, and the connection is reset. */
    timeoutMs?: number);
    send(cmd: string, args?: Arg[]): Promise<Reply>;
    /** Sends commands together; replies come back in order (errors as ReplyError values). */
    pipeline(cmds: Arg[][]): Promise<Reply[]>;
    close(): void;
    private enqueue;
    private flush;
    private open;
    private arm;
    private reset;
    private onData;
}
interface Target {
    host: string;
    port: number;
    tls: boolean;
    path?: string;
    user: string;
    pass: string;
    db?: string | null;
}
/** Parses a connection URL. redis+unix://user:pass@/path is not a WHATWG URL, so unix ones are split by hand. */
export declare function target(url: string): Target;
export {};
