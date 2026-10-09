# Managed boxes (ShipTiffin)

A managed box is a Tiffin box that **shiptiffin.com** sets up in your own Hetzner Cloud
account. The server, its disk and everything on it are yours, on your Hetzner bill. ShipTiffin
charges $19 a month per box ($12 for the first 100 customers, locked for 24 months) for the
managed extras:

- Tiffin installed, then kept up to date (the box installs signed releases itself);
- monitoring from outside, with an email when the box stops answering;
- a free `<name>.shiptiffin.app` address with HTTPS;
- resizing from your account;
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
   minutes. If setup fails before Tiffin is installed, it removes the address first and
   then deletes what it made (the server only once the address is gone), and you get an
   email. Once Tiffin is installed nothing is ever deleted: a later step that fails leaves
   the server, its data and the address, marks the box *needs attention* in your account
   and emails you and us.
6. **Ready.** The box is ready once its dashboard answers over HTTPS with a valid
   certificate. Until then the page says *certificate pending*; we check every minute and
   email you when it's ready.
7. **Open your dashboard.** Until you first sign in, the button signs you in with a
   one-time link your box made (below). Add a passkey on the box then: after that you sign
   in on the box itself, and the button just opens its sign-in page.

## Your Hetzner key

- **Used for one job, then forgotten.** The key stays in your browser until you click
  Create. The website then seals it to the setup worker's public key (X25519 with a fresh
  key per value, AES-256-GCM, bound to your box so it opens nowhere else): the website
  itself can't open it. Only the worker can, a separate service whose secrets the website
  never sees. The worker clears it when the job ends, whether setup worked or not;
  anything older than two hours is wiped regardless. What stays is a fingerprint: the
  first 12 hex characters of its SHA-256.
- **Never stored.** Nothing keeps your key past its job. A resize, or deleting the server,
  asks for a key again, uses it for that job and forgets it the same way.
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
- **One-time sign-in links, until you sign in.** ShipTiffin never holds your box's owner
  token: it stays on the box. Your box makes one-time owner sign-in links and we keep only
  the newest. Your box enforces them: a link works once, and the box refuses it 24 hours
  after it made it, even if a copy leaks later. The first is made at setup; if the
  dashboard took more than an hour longer to become ready (a slow certificate), the box
  makes a fresh one once it is, so you get the full 24 hours from readiness. If a link
  expires unused, *Get a new sign-in link* in your account (or *Open your dashboard*) asks
  your box for another: it makes it and sends it with its next check-in (every few minutes
  while it waits for your first sign-in). No SSH is involved. We keep the link until your
  box tells us you signed in (so a first click that didn't get through can be tried
  again), and delete it then, when it expires, or when you click *Forget the sign-in link*
  (which also tells us never to ask for another). You count as signed in once your new
  session makes its first request after the sign-in itself (a sign-in whose answer never
  reached your browser doesn't count, so a new link can still be made). From then on your
  box makes no more links for us, and any it made that are still unused stop working: we
  have no way to sign in to your box.
- **Updates are pulled, never pushed.** The box reads the signed release manifest itself
  and installs new releases in its maintenance window, 03:00 server time (UTC) unless you
  move it ([Tiffin's own updates](https://shiptiffin.com/docs/quickstart.md#tiffins-own-updates)); ShipTiffin never
  connects to it to install anything. The only inbound requests from ShipTiffin are the monitor's: `GET
  https://dashboard.<name>.shiptiffin.app/v1/health` every five minutes.
- **The check-in.** Every six hours the box posts its Tiffin version, uptime, the
  *names* of any failing status checks and whether its owner has signed in yet to
  shiptiffin.com, with its licence (an ed25519-signed token naming the box and its setup;
  it opens nothing on the box). No project names, data, logs or visitors. The answer says
  whether the subscription is active; until the owner first signs in it may also ask for a
  fresh sign-in link (above) and for the next check-in within minutes. A check-in counts only with the licence of the box's latest setup, sent from the
  box's own address.
- **Support access** is yours to grant: support never logs in by default, and there is no
  button for it yet. Write to hello@shiptiffin.com and we arrange it with you by email: you
  add a temporary SSH key and open port 22 for us, and remove both afterwards.
- **Deleting the server** happens only when you ask in your account, type the box's name and
  paste a key right then. The address goes first, then the server and what else carries
  your box's label; the data volume stays unless you tick that too.
- **A resize always ends with the server running.** A resize that stops half way (our
  worker restarting, say) is picked up and starts the server again with that resize's key,
  even if your subscription ended meanwhile. If we hold no key by then (a resize key is
  forgotten after two hours), your account says so and we email you to start the server in
  the Hetzner console.

## The address

The box's domain is `<name>.shiptiffin.app`: the dashboard is
`dashboard.<name>.shiptiffin.app` and apps are `<project>.<name>.shiptiffin.app`, as on any
box with its own domain ([domains](https://shiptiffin.com/docs/domains.md)). ShipTiffin keeps two DNS records per box,
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
with an email when it starts, a week before it goes and when it goes. The address never
goes until the week-before warning was accepted by our mail server at least seven days
earlier: a warning that fails is sent again a day later, and the address stays meanwhile.
Point a domain of your own at the box before then. **Renew** in your account starts a new
subscription for the same box, whatever stage it reached (set up, waiting for Hetzner, or
a setup that failed); everything turns back on, and the address returns within a few hours
(at the box's next check-in).

**Money back.** Ask within 14 days of your first payment (write to hello@shiptiffin.com)
and we refund it in full and end the subscription; the address then keeps its 30 days.

**If the box goes quiet.** A box that hasn't checked in for 72 hours has its address
parked (you get an email): if its server was deleted, Hetzner may give the IP to someone
else, who must not get your name with it. Start the server and the address comes back at
its next check-in.

**Stop managed service** (in your account, under *Stop or delete this box*) ends the
subscription at once, removes the address and any key we hold, and leaves the server exactly
as it is: an ordinary Tiffin box of yours, which goes on installing updates by itself.

## Abuse

The servers are in customers' own Hetzner accounts; the `shiptiffin.app` addresses are
ours. Report one at shiptiffin.com/abuse or to abuse@shiptiffin.com. ShipTiffin removes an
address used for phishing or malware (the box's owner is told why); the server is untouched.
