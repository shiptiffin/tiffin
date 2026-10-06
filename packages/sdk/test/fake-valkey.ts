// A stand-in Valkey for tests: speaks RESP2 over TCP, keeps data with
// @shiptiffin/sdk/next's memoryRedis, and logs every command it receives.
import { createServer, type Server, type Socket } from "node:net";
import { memoryRedis } from "../src/next";

type Data = { send(cmd: string, args: (string | Uint8Array)[]): Promise<unknown> };

export interface FakeValkey {
  url: string;
  /** Commands received, as "CMD key". */
  log: string[];
  /** Stops answering (replies are dropped) while true. */
  mute: boolean;
  close(): Promise<void>;
}

export async function startFakeValkey(port = 0): Promise<FakeValkey> {
  const data = memoryRedis() as unknown as Data;
  const sockets = new Set<Socket>();
  const fake: FakeValkey = { url: "", log: [], mute: false, close: async () => {} };
  const server: Server = createServer((sock) => {
    sockets.add(sock);
    sock.on("close", () => sockets.delete(sock));
    let buf: Buffer = Buffer.alloc(0);
    let chain = Promise.resolve();
    sock.on("data", (d: Buffer) => {
      buf = buf.length ? Buffer.concat([buf, d]) : d;
      for (;;) {
        const r = request(buf);
        if (!r) break;
        buf = buf.subarray(r.end);
        const [cmd, ...args] = r.args;
        const name = cmd!.toString().toUpperCase();
        fake.log.push(`${name} ${args[0]?.toString() ?? ""}`.trim());
        chain = chain.then(async () => {
          let out: Buffer;
          if (name === "AUTH" || name === "SELECT" || name === "PING") out = Buffer.from("+OK\r\n");
          else if (name === "HELLO") out = Buffer.concat([Buffer.from("%2\r\n"), reply("server"), reply("valkey"), reply("proto"), reply(3)]); // Bun's client
          else {
            try {
              out = reply(await data.send(name, name === "SET" ? [args[0]!.toString(), args[1]!, ...args.slice(2).map(String)] : args.map(String)));
            } catch (e) {
              out = Buffer.from(`-ERR ${(e as Error).message}\r\n`);
            }
          }
          if (!fake.mute) sock.write(out);
        });
      }
    });
  });
  await new Promise<void>((r) => server.listen(port, "127.0.0.1", r));
  const addr = server.address() as { port: number };
  fake.url = `redis://user:pw@127.0.0.1:${addr.port}`;
  fake.close = () =>
    new Promise<void>((r) => {
      for (const s of sockets) s.destroy();
      server.close(() => r());
    });
  return fake;
}

// `bun test/fake-valkey.ts [port]` runs one on its own, e.g. for benchmarks.
if (import.meta.main) console.log((await startFakeValkey(Number(process.argv[2] ?? 0))).url);

function request(buf: Buffer): { args: Buffer[]; end: number } | undefined {
  if (buf.length === 0 || buf[0] !== 42) return undefined;
  let eol = buf.indexOf("\r\n");
  if (eol < 0) return undefined;
  const n = Number(buf.toString("latin1", 1, eol));
  let at = eol + 2;
  const args: Buffer[] = [];
  for (let i = 0; i < n; i++) {
    eol = buf.indexOf("\r\n", at);
    if (eol < 0) return undefined;
    const len = Number(buf.toString("latin1", at + 1, eol));
    at = eol + 2;
    if (at + len + 2 > buf.length) return undefined;
    args.push(buf.subarray(at, at + len));
    at += len + 2;
  }
  return { args, end: at };
}

function reply(v: unknown): Buffer {
  if (v == null) return Buffer.from("$-1\r\n");
  if (typeof v === "number") return Buffer.from(`:${v}\r\n`);
  if (typeof v === "string" || v instanceof Uint8Array) {
    const b = typeof v === "string" ? Buffer.from(v) : Buffer.from(v);
    return Buffer.concat([Buffer.from(`$${b.length}\r\n`), b, Buffer.from("\r\n")]);
  }
  const items = Array.isArray(v) ? v : Object.entries(v as Record<string, unknown>).flat();
  return Buffer.concat([Buffer.from(`*${items.length}\r\n`), ...items.map(reply)]);
}
