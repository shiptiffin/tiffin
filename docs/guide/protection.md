# Protection

On by default:

- **Rate limits** per IP for every app, a strict limit on sign-in endpoints, and a
  generous one for the dashboard.
- **CrowdSec** reads the edge's access log and bans scanners and brute-forcers; bans are
  enforced at the edge. `tiffin protect unban <ip>`.
- **Firewall:** only SSH and the edge ports are open; everything else the box runs is
  private.

When you need them:

- **Under attack:** `tiffin protect under-attack --on --minutes 60` puts a small
  proof-of-work challenge in front of every app (real browsers pass in a fraction of a
  second; API calls with tokens are never challenged) and tightens limits. It turns
  itself off.
- **WAF:** Coraza with the OWASP Core Rule Set, opt-in (`PUT /v1/protect {waf: true}`).

On a server, Cloudflare in front absorbs large attacks; that upgrade comes later.
