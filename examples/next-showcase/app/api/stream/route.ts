import { connection } from "next/server";

// Server-Sent Events: one event every 200 ms, ten in all. The headers are
// what most apps send; nothing here asks proxies not to compress.
export async function GET(req: Request) {
  await connection();
  const enc = new TextEncoder();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      let i = 0;
      const tick = () => {
        c.enqueue(enc.encode(`id: ${i}\nevent: tick\ndata: {"i":${i},"t":${Date.now()}}\n\n`));
        if (++i === 10) return c.close();
        timer = setTimeout(tick, 200);
      };
      tick();
      req.signal.addEventListener("abort", () => clearTimeout(timer));
    },
    cancel() {
      clearTimeout(timer);
    },
  });
  return new Response(stream, {
    headers: { "content-type": "text/event-stream; charset=utf-8", "cache-control": "no-cache" },
  });
}
