import http from "node:http";
import { greeting } from "@vx/words";

http
  .createServer((req, res) => res.end(`vx-mono-api ${req.url} ${greeting}`))
  .listen(Number(process.env.PORT ?? 3000));
