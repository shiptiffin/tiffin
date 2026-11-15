// An in-memory stand-in for Valkey with exactly the commands the session
// driver and cache provider send. Plug it into the SDK with
// kv({ client: fake, prefix }).raw(). `down` makes every call fail, `delayMs`
// makes every reply late, and `log` records the commands.
import { kv } from "@shiptiffin/sdk/kv";
import type { Store } from "../src/store";

export interface FakeRedis {
  send(cmd: string, args: string[]): Promise<unknown>;
  data: Map<string, { v: string; exp: number }>;
  log: string[];
  down: boolean;
  delayMs: number;
  /** Moves the fake's clock (expiries) forward. */
  advance(ms: number): void;
}

export function fakeRedis(): FakeRedis {
  let skew = 0;
  const now = () => Date.now() + skew;
  const data = new Map<string, { v: string; exp: number }>();
  const live = (k: string) => {
    const e = data.get(k);
    if (e && e.exp && e.exp <= now()) {
      data.delete(k);
      return undefined;
    }
    return e;
  };
  const f: FakeRedis = {
    data,
    log: [],
    down: false,
    delayMs: 0,
    advance(ms) {
      skew += ms;
    },
    async send(cmd, args) {
      const c = cmd.toUpperCase();
      f.log.push(`${c} ${args[0] ?? ""}`.trim());
      if (f.delayMs) await new Promise((r) => setTimeout(r, f.delayMs));
      if (f.down) throw new Error("connect ECONNREFUSED (fake)");
      switch (c) {
        case "PING":
          return "PONG";
        case "GET":
          return live(args[0]!)?.v ?? null;
        case "MGET":
          return args.map((k) => live(k)?.v ?? null);
        case "SET": {
          const [k, v, ...o] = args as [string, string, ...string[]];
          const up = o.map((x) => x.toUpperCase());
          if (up.includes("NX") && live(k)) return null;
          if (up.includes("XX") && !live(k)) return null;
          let exp = 0;
          const ex = up.indexOf("EX");
          const px = up.indexOf("PX");
          if (ex >= 0) exp = now() + Number(o[ex + 1]) * 1000;
          if (px >= 0) exp = now() + Number(o[px + 1]);
          data.set(k, { v, exp });
          return "OK";
        }
        case "DEL":
          return args.reduce((n, k) => n + (live(k) && data.delete(k) ? 1 : 0), 0);
        case "GETEX": {
          const e = live(args[0]!);
          if (!e) return null;
          const i = args.findIndex((x) => x.toUpperCase() === "EX");
          if (i >= 0) e.exp = now() + Number(args[i + 1]) * 1000;
          return e.v;
        }
        case "EXPIRE": {
          const e = live(args[0]!);
          if (!e) return 0;
          e.exp = now() + Number(args[1]) * 1000;
          return 1;
        }
        case "TTL": {
          const e = live(args[0]!);
          if (!e) return -2;
          return e.exp ? Math.ceil((e.exp - now()) / 1000) : -1;
        }
        default:
          throw new Error(`ERR unknown command '${cmd}' (fake)`);
      }
    },
  };
  return f;
}

/** The SDK's raw store over a fake, with the box's key prefix. */
export function fakeStore(prefix = "p_test:"): { fake: FakeRedis; store: Store } {
  const fake = fakeRedis();
  return { fake, store: kv({ client: fake, prefix }).raw() as unknown as Store };
}
