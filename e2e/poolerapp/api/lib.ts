// Drives database clients for the pooler test (copied into each app).
// A client is a function that runs query number i and returns i back from
// the database, so answers mixed up between prepared statements show.

export type Client = { name: string; query: (i: number) => Promise<number> };

type Stats = { queries: number; errors: number; maxMs: number; firstError?: string };

/** Runs n queries per client, conc at a time, and reports each client. */
export async function check(clients: Client[], n = 60, conc = 12) {
  const out: Record<string, { ok: boolean; ms: number; error?: string }> = {};
  for (const c of clients) {
    const t0 = performance.now();
    try {
      for (let i = 0; i < n; i += conc) {
        const got = await Promise.all(Array.from({ length: Math.min(conc, n - i) }, (_, k) => c.query(i + k + 1)));
        got.forEach((v, k) => {
          if (Number(v) !== i + k + 1) throw new Error(`query ${i + k + 1} answered ${v}`);
        });
      }
      out[c.name] = { ok: true, ms: Math.round(performance.now() - t0) };
    } catch (e) {
      out[c.name] = { ok: false, ms: Math.round(performance.now() - t0), error: String(e) };
    }
  }
  return out;
}

let running = false;
let loops: Promise<void>[] = [];
let stats: Record<string, Stats> = {};

/** Starts conc loops per client, each querying every pause ms until stop. */
export function startLoad(clients: Client[], conc = 3, pause = 20) {
  if (running) return;
  running = true;
  stats = {};
  loops = [];
  for (const c of clients) {
    const s: Stats = { queries: 0, errors: 0, maxMs: 0 };
    stats[c.name] = s;
    for (let w = 0; w < conc; w++) {
      loops.push(
        (async () => {
          let i = w * 1_000_000;
          while (running) {
            i++;
            const t0 = performance.now();
            try {
              const v = await c.query(i);
              if (Number(v) !== i) throw new Error(`query ${i} answered ${v}`);
              s.queries++;
            } catch (e) {
              s.errors++;
              s.firstError ??= String(e);
            }
            s.maxMs = Math.max(s.maxMs, Math.round(performance.now() - t0));
            await Bun.sleep(pause);
          }
        })(),
      );
    }
  }
}

export async function stopLoad() {
  running = false;
  await Promise.all(loops);
  return stats;
}

/** Routes /clients, /load/start and /load/stop. */
export function routes(clients: Client[], pathname: string): Promise<Response> | null {
  if (pathname === "/clients") return check(clients).then((r) => Response.json(r));
  if (pathname === "/load/start") {
    startLoad(clients);
    return Promise.resolve(Response.json({ started: clients.map((c) => c.name) }));
  }
  if (pathname === "/load/stop") return stopLoad().then((r) => Response.json(r));
  return null;
}
