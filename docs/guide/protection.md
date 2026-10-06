# Protection

On by default:

- **Rate limits** per IP for every app, a strict limit on sign-in endpoints, and a
  generous one for the dashboard. Next.js Server Actions posted from a page such as
  `/sign-in` don't count as sign-in attempts; the auth engine limits the sign-ins they make.
- **Security headers** on every response: `X-Content-Type-Options`, `Referrer-Policy`,
  HSTS, and no framing (`X-Frame-Options: DENY`, plus a `frame-ancestors 'none'` CSP when
  the app sends no CSP) unless the app sends its own `X-Frame-Options` or a CSP with
  `frame-ancestors`.
- **CrowdSec** reads the edge's access log and bans scanners and brute-forcers; bans are
  enforced at the edge. `tiffin protect unban <ip>`.
- **Slow clients:** a connection whose request body stops arriving, or whose client stops
  reading the response, for 5 minutes is closed (a slowloris attack holds connections
  open that way). Request headers must arrive within a minute, and an idle keep-alive
  connection closes after 5 minutes. A quiet app is never cut for it: each request may
  take up to its app's time limit (`timeoutSeconds`, 15 minutes by default, up to 24
  hours; see [Apps](apps.md#programs-folders-and-long-requests)), and a stream may pause
  for as long as it likes within it.
- **Firewall:** only SSH and the edge ports are open (plus UDP 443 for HTTP/3 on a
  server); everything else the box runs is private.

When you need them:

- **Under attack:** `tiffin protect under-attack --on --minutes 60` puts a small
  proof-of-work challenge in front of every app (real browsers pass in a fraction of a
  second; API calls with tokens are never challenged) and tightens limits. It turns
  itself off.
- **WAF:** Coraza with the OWASP Core Rule Set, opt-in (`PUT /v1/protect {waf: true}`).
  It inspects the first 12.5 MB of a request body and passes the rest through, so
  uploads of any size still reach the app.

On a server, Cloudflare in front absorbs large attacks; that upgrade comes later.
