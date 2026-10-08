# Managed boxes (ShipTiffin)

A managed box is a Tiffin box that **shiptiffin.com** sets up in your own Hetzner Cloud
account. The server, its disk and everything on it are yours, on your Hetzner bill. ShipTiffin
charges $19 a month per box ($12 for the first 100 customers, locked for 24 months) for the
managed extras:

- Tiffin installed, then kept up to date (the box installs signed releases itself);
- monitoring from outside, with an email when the box stops answering;
- a free `<name>.shiptiffin.app` address with HTTPS;
- one-click resize;
- support by email.

A box made with `tiffin up` is not managed and none of this runs on it.

## How setup works

1. **Account.** Sign in at shiptiffin.com/start with an email link, Google or GitHub. No
   password.
2. **Pay.** Stripe Checkout, one subscription per box, by card. The box is ready for setup
   once the first payment has gone through; a second payment for the same box (two tabs)
   is cancelled and refunded automatically.
3. **Connect Hetzner.** In the Hetzner Cloud Console make a **new project just for
   ShipTiffin**, then Security → API tokens → Generate API token → **Read & Write**, and
   paste it. The page checks it at once: that Hetzner accepts it, that it can write (with
   one request that creates nothing), how many servers the project already holds, and
   which sizes Hetzner sells you where, at your account's prices and stock.
4. **Choose.** A name (your address is `<name>.shiptiffin.app`), a size and a place. We
   suggest `cx23` in the first EU location that has stock, then `cax11`, `cx33` and
   `cax21`; US locations offer CPX sizes. Each comes with a 40 GB data volume.
5. **Create.** A worker on ShipTiffin's own box makes the server, firewall, volume and a
   setup SSH key in your project (all labelled `tiffin-box=<name>` and
   `shiptiffin-box=<box id>`; it never touches anything without that second label), points
   the address at the server, installs Tiffin the way `tiffin up --provider hetzner` does,
   then removes its access (below). The page shows each step live; it takes about five
   minutes. If setup fails, it removes the address and deletes what it made, and you get
   an email.
6. **Ready.** The box is ready once its dashboard answers over HTTPS with a valid
   certificate. Until then the page says *certificate pending*; we check every minute and
   email you when it's ready.
7. **Open your dashboard.** The first time, the button signs you in with a one-time link
   your box made at setup (below). Add a passkey on the box then: after that you sign in on
   the box itself, and the button just opens its sign-in page.

## Your Hetzner key

- **Used for setup, then forgotten.** The key stays in your browser until you click
  Create. The website then seals it to the setup worker's public key (X25519 with a fresh
  key per value, AES-256-GCM, bound to your box so it opens nowhere else): the website
  itself can't open it. Only the worker can, a separate service whose secrets the website
  never sees. The worker clears it when the job ends, whether setup worked or not;
  anything older than two hours is wiped regardless. What stays is a fingerprint: the
  first 12 hex characters of its SHA-256.
- **Kept only if you ask.** Tick *Keep my key so I can resize in one click* and the sealed
  key is stored with your box. **Remove** in your account deletes it at once.
- **Resizing without a stored key** asks for a key, uses it for that resize and forgets it.
- **Every call is logged.** Each request ShipTiffin makes with your key (method, path,
  time, Hetzner's answer) is listed in your account, from the first check to the last
  resize. The key itself is never logged.
- **What it can't do.** A token reaches only the Hetzner project it was made in, never your
  Hetzner login, password or billing. Delete it in Hetzner (Security → API tokens) any
  time; the box keeps running.

## What ShipTiffin can and can't do on your server

- **No login after setup.** Setup logs in as root with an SSH key made for that one job,
  through a firewall rule that lets only the worker's own address reach port 22. After the
  install it deletes the key's line from `/root/.ssh/authorized_keys`, deletes the SSH key
  object from your project, removes the firewall's SSH rule, checks all three are gone, and
  deletes the private key (a worker that stops mid-setup deletes leftover keys when it
  starts again, and the failed setup is cleaned up). Port 22 is then closed to everyone;
  HTTP, HTTPS and ping stay open. (Hetzner's cloud-init adds keys only on a server's first
  boot, so a reboot or a resize does not bring the key back.)
- **One sign-in link, for 24 hours.** ShipTiffin never holds your box's owner token: it
  stays on the box. At setup the box makes a one-time owner sign-in link and the worker
  keeps only that. Your box enforces it: the link works once, and the box refuses it 24
  hours after setup even if a copy leaks later. We hand it to you at your first *Open your
  dashboard* and delete it then (or when it expires, or when you click *Forget the sign-in
  link*). After that, we have no way to sign in to your box.
- **Updates are pulled, never pushed.** The box reads the signed release manifest itself
  and installs new releases in its maintenance window, 03:00 server time (UTC) unless you
  move it ([Tiffin's own updates](quickstart.md#tiffins-own-updates)); ShipTiffin never
  connects to it to install anything. The only inbound requests from ShipTiffin are the monitor's: `GET
  https://dashboard.<name>.shiptiffin.app/v1/health` every five minutes.
- **The check-in.** Every six hours the box posts its Tiffin version, uptime and the
  *names* of any failing status checks to shiptiffin.com, with its licence (an
  ed25519-signed token naming the box and its setup; it opens nothing on the box). No
  project names, data, logs or visitors. The answer says whether the subscription is
  active. A check-in counts only with the licence of the box's latest setup, sent from the
  box's own address.
- **Support access** is yours to grant: support never logs in by default, and there is no
  button for it yet. Write to hello@shiptiffin.com and we arrange it with you by email: you
  add a temporary SSH key and open port 22 for us, and remove both afterwards.
- **Deleting the server** happens only when you ask in your account, type the box's name and
  paste a key right then. The address goes first, then the server and what else carries
  your box's label; the data volume stays unless you tick that too.

## The address

The box's domain is `<name>.shiptiffin.app`: the dashboard is
`dashboard.<name>.shiptiffin.app` and apps are `<project>.<name>.shiptiffin.app`, as on any
box with its own domain ([domains](domains.md)). ShipTiffin keeps two DNS records per box,
`<name>.shiptiffin.app` and `*.<name>.shiptiffin.app`, pointing at the server (A, and AAAA
with IPv6), DNS only. The box gets a certificate for each name it serves over HTTP-01, so it
holds no DNS credential. Your own domain works as on any box (`tiffin domain set`); the
shiptiffin.app address then just stays as a second name.

Names are 3 to 30 lowercase letters, digits and dashes, start with a letter, have no double
dash, and are unique. Names such as `www`, `admin`, `dashboard` and well-known brands are
reserved.

## If you stop paying

Nothing happens to your server or apps, ever, over billing. When the subscription ends (or
stays unpaid after Stripe's retries): automatic updates pause (the box's Updates page says
why), monitoring emails and support stop, and the address keeps working for **30 days**,
with an email when it starts, a week before it goes and when it goes (the address never
goes before that warning was sent). Point a domain of your own at the box before then.
**Renew** in your account starts a new subscription for the same box; everything turns
back on, and the address returns within a few hours (at the box's next check-in).

**Money back.** Ask within 14 days of your first payment (write to hello@shiptiffin.com)
and we refund it in full and end the subscription; the address then keeps its 30 days.

**If the box goes quiet.** A box that hasn't checked in for 72 hours has its address
parked (you get an email): if its server was deleted, Hetzner may give the IP to someone
else, who must not get your name with it. Start the server and the address comes back at
its next check-in.

**Release from ShipTiffin** (in your account) ends the subscription at once, removes the
address and any key we hold, and leaves the server exactly as it is: an ordinary Tiffin box
of yours, which goes on installing updates by itself.

## Abuse

The servers are in customers' own Hetzner accounts; the `shiptiffin.app` addresses are
ours. Report one at shiptiffin.com/abuse or to abuse@shiptiffin.com. ShipTiffin removes an
address used for phishing or malware (the box's owner is told why); the server is untouched.

## Running the control plane (owner)

Two projects on ShipTiffin's own box, so the worker's secrets never reach the website:

- **`website`** (`site/tiffin.config.ts`): the Next.js app (`web`), previews off. Its
  Postgres holds the `cloud_*` tables (the worker creates them).
- **`cloud`** (`cmd/tiffin-cloud/tiffin.config.ts`): the Go worker (`worker`), previews off.
  It reaches the website's database with that project's `DATABASE_URL`, given to it as
  `CONTROL_DATABASE_URL` (projects can't share a role; see limits).

Secrets, by name:

| Project | Secret | |
|---|---|---|
| website | `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` | test mode until `STRIPE_LIVE=1` |
| website | `STRIPE_COUPON_FOUNDING`, `STRIPE_PRICE_MONTHLY`, `STRIPE_CHECKOUT_LINK` | optional |
| website | `CLOUD_SEAL_PUBLIC`, `CLOUD_LICENCE_PUBLIC` | public keys only |
| website | `CLOUD_ADMIN_USER_IDS` | account ids allowed into /admin (verified email) |
| website | `CLOUD_ABUSE_NOTIFY`, `EARLY_ACCESS_NOTIFY`, `SITE_URL` | optional |
| cloud | `CONTROL_DATABASE_URL` | the website project's `DATABASE_URL` |
| cloud | `CLOUD_SEAL_KEY`, `CLOUD_LICENCE_KEY` | private keys |
| cloud | `CLOUDFLARE_API_TOKEN` | Zone · DNS · Edit on shiptiffin.app only |
| cloud | `CLOUD_SSH_FROM`, `CLOUD_RELEASE_SOURCE`, `CLOUD_CONTROL_URL`, `CLOUD_ZONE` | optional |

Setting it up:

1. `go run ./cmd/tiffin-cloud keygen` prints both key pairs, labelled by project.
2. Apply `cmd/tiffin-cloud/tiffin.config.ts` (`tiffin apply` from that folder) to create
   the `cloud` project, and remove the old `cloud` app from the `website` project (the
   website's config no longer lists it). Delete `CLOUD_KEK`, `CLOUD_LICENCE_KEY`,
   `CLOUDFLARE_API_TOKEN` and `CLOUD_ADMIN_EMAILS` from the website's secrets.
3. Set the cloud project's secrets (above), then the website's.
4. In Stripe, point the webhook at `https://shiptiffin.com/api/stripe/webhook` with
   `checkout.session.completed`, `checkout.session.async_payment_succeeded`,
   `checkout.session.async_payment_failed`, `customer.subscription.created`,
   `customer.subscription.updated`, `customer.subscription.deleted`, `invoice.paid`,
   `invoice.payment_failed` and `charge.refunded`.
5. Sign in at shiptiffin.com, read your account id (the website project's Database
   browser, `tiffin_auth` users), and set `CLOUD_ADMIN_USER_IDS` to it.
6. If `cloud_*` tables exist from an earlier build, drop them: the worker refuses to start
   on them (before launch nothing in them matters). /start shows the sign-up list until
   every website secret is set and the worker has made its tables.
