// tiffin-auth: the box's auth engine. One process serves every project.
//
//   tiffin-auth serve   [--config F] [--listen 127.0.0.1:7393] [--admin-socket S]
//   tiffin-auth migrate --project P [--config F]
//   tiffin-auth version
import { chmodSync, existsSync, mkdirSync, unlinkSync } from "node:fs";
import { dirname } from "node:path";
import { adminHandler } from "./admin";
import { closeTransports } from "./mail";
import { migrate } from "./migrate";
import { Registry } from "./registry";
import { publicHandler } from "./server";

export const VERSION = "0.1.0";

function flag(args: string[], name: string, env: string, def: string): string {
  const i = args.indexOf(name);
  if (i >= 0 && args[i + 1]) return args[i + 1]!;
  const eq = args.find((a) => a.startsWith(name + "="));
  if (eq) return eq.slice(name.length + 1);
  return process.env[env] || def;
}

async function main(argv: string[]) {
  const [cmd = "serve", ...args] = argv;
  const configPath = flag(args, "--config", "TIFFIN_AUTH_CONFIG", "/var/lib/tiffin/auth/engine.json");
  switch (cmd) {
    case "version":
      console.log(VERSION);
      return;
    case "migrate": {
      const project = flag(args, "--project", "TIFFIN_PROJECT", "");
      const reg = new Registry(configPath);
      const cfg = reg.projectConfig(project);
      if (!cfg) throw new Error(`project ${project || "(none)"} isn't in ${configPath}`);
      console.log(JSON.stringify(await migrate(project, cfg, reg.pool(project)!)));
      await reg.closeAll();
      return;
    }
    case "serve": {
      const listen = flag(args, "--listen", "TIFFIN_AUTH_LISTEN", "127.0.0.1:7393");
      const socket = flag(args, "--admin-socket", "TIFFIN_AUTH_ADMIN_SOCKET", "/run/tiffin-auth/admin.sock");
      const [hostname, port] = [listen.slice(0, listen.lastIndexOf(":")), Number(listen.slice(listen.lastIndexOf(":") + 1))];
      const reg = new Registry(configPath);
      const handler = publicHandler(reg);
      const pub = Bun.serve({ hostname, port, fetch: handler, idleTimeout: 30 });
      // Extra addresses from the config (the runtime's bridge IP), rebound on reload.
      const extra = new Map<string, ReturnType<typeof Bun.serve>>();
      const syncListeners = (want: string[]) => {
        for (const [addr, srv] of extra) {
          if (!want.includes(addr)) {
            srv.stop();
            extra.delete(addr);
          }
        }
        for (const addr of want) {
          if (extra.has(addr) || addr === listen) continue;
          const i = addr.lastIndexOf(":");
          try {
            extra.set(addr, Bun.serve({ hostname: addr.slice(0, i).replace(/^\[|\]$/g, ""), port: Number(addr.slice(i + 1)), fetch: handler, idleTimeout: 30 }));
          } catch (err) {
            console.error(JSON.stringify({ level: "error", msg: "can't listen", addr, err: String(err) }));
          }
        }
      };
      reg.onChange = (c) => syncListeners(c.listen);
      syncListeners(reg.listen());
      mkdirSync(dirname(socket), { recursive: true, mode: 0o700 });
      if (existsSync(socket)) unlinkSync(socket);
      const admin = Bun.serve({ unix: socket, fetch: adminHandler(reg) });
      chmodSync(socket, 0o600);
      console.log(JSON.stringify({ level: "info", msg: "tiffin-auth serving", version: VERSION, listen: `${hostname}:${pub.port}`, adminSocket: socket, projects: reg.projects() }));
      const stop = async () => {
        pub.stop();
        admin.stop();
        for (const srv of extra.values()) srv.stop();
        await reg.closeAll();
        closeTransports();
        process.exit(0);
      };
      process.on("SIGTERM", stop);
      process.on("SIGINT", stop);
      process.on("SIGHUP", () => reg.reload());
      return;
    }
    default:
      console.error(`unknown command ${cmd}; use serve, migrate or version`);
      process.exit(2);
  }
}

main(process.argv.slice(2)).catch((err) => {
  console.error(JSON.stringify({ level: "error", msg: String(err instanceof Error ? err.message : err) }));
  process.exit(1);
});
