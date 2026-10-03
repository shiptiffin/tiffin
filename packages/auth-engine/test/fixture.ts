// A real engine on a throwaway Postgres, for the Go module's contract test
// (internal/mod/auth/engine_test.go). Prints one JSON line with the database
// URL and the public port, then serves until killed.
//
//   bun test/fixture.ts --config <path> --socket <path>
import { chmodSync, existsSync, unlinkSync } from "node:fs";
import { adminHandler } from "../src/admin";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { freshDatabase, stopCluster } from "./pg";

const arg = (n: string) => {
  const i = process.argv.indexOf(n);
  if (i < 0 || !process.argv[i + 1]) throw new Error(`missing ${n}`);
  return process.argv[i + 1]!;
};
const config = arg("--config");
const socket = arg("--socket");

const databaseUrl = await freshDatabase("go_contract");
const reg = new Registry(config);
const pub = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: publicHandler(reg) });
if (existsSync(socket)) unlinkSync(socket);
const admin = Bun.serve({ unix: socket, fetch: adminHandler(reg) });
chmodSync(socket, 0o600);
console.log(JSON.stringify({ databaseUrl, port: pub.port }));

const stop = async () => {
  pub.stop(true);
  admin.stop(true);
  await reg.closeAll();
  await stopCluster();
  process.exit(0);
};
process.on("SIGTERM", stop);
process.on("SIGINT", stop);
// Die with the parent test process.
setInterval(() => {
  if (process.ppid === 1) void stop();
}, 1000);
