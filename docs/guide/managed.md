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
2. **Pay.** Stripe Checkout, one subscription per box.
3. **Connect Hetzner.** In the Hetzner Cloud Console make a **new project just for
   ShipTiffin**, then Security → API tokens → Generate API token → **Read & Write**, and
   paste it. The page checks it at once: that Hetzner accepts it, that it can write (with
   one request that creates nothing), how many servers the project already holds, and
   which sizes Hetzner sells you where, at your account's prices and stock.
4. **Choose.** A name (your address is `<name>.shiptiffin.app`), a size and a place. We
   suggest `cx23` in the first EU location that has stock, then `cax11`, `cx33` and
   `cax21`; US locations offer CPX sizes. Each comes with a 40 GB data volume.
5. **Create.** A worker on ShipTiffin's own box makes the server, firewall, volume and a
   setup SSH key in your project (all labelled `tiffin-box=<name>`), points the address at
   the server, installs Tiffin the way `tiffin up --provider hetzner` does, then removes
   its access (below). The page shows each step live; it takes about five minutes.
6. **Open your dashboard.** The button signs you straight in. Add a passkey on the box the
   first time, so you can sign in on your own from then on.

## Your Hetzner key

- **Used for setup, then forgotten.** The key stays in your browser until you click
  Create. It then travels to the worker sealed (envelope encryption: a fresh AES-256-GCM
  key per value, itself encrypted with a master key kept in ShipTiffin's secrets, never in
  its database, and bound to your box so it opens nowhere else). The worker clears it when
  the job ends, whether setup worked or not; anything older than two hours is wiped
  regardless. What stays is a fingerprint: the first 12 hex characters of its SHA-256.
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
  deletes the private key. Port 22 is then closed to everyone; HTTP, HTTPS and ping stay
  open. (Hetzner's cloud-init adds keys only on a server's first boot, so a reboot or a
  resize does not bring the key back.)
- **A short-lived sign-in key.** The worker keeps the new box's owner token, sealed, only so
  *Open your dashboard* can make one-time sign-in links. It is deleted a day after the
  first open, after 7 days at most, or when you click *Forget the setup sign-in key*.
- **Updates are pulled, never pushed.** The box reads the signed release manifest itself
  and installs new releases in its maintenance window, 03:00 server time (UTC) unless you
  move it ([Tiffin's own updates](quickstart.md#tiffins-own-updates)); ShipTiffin never
  connects to it to install anything. The only inbound requests from ShipTiffin are the monitor's: `GET
  https://dashboard.<name>.shiptiffin.app/v1/health` every five minutes.
- **The daily check-in.** The box posts its Tiffin version, uptime and the *names* of any
  failing status checks to shiptiffin.com once a day, with its licence (an ed25519-signed
  token naming the box; it opens nothing on the box). No project names, data, logs or
  visitors. The answer says whether the subscription is active.
- **Support access** is yours to grant: support never logs in by default. If you want us to
  look at the server itself, you add a temporary SSH key and open port 22 for us, and remove
  both afterwards.
- **Deleting the server** happens only when you ask in your account, type the box's name and
  paste a key right then. The data volume stays unless you tick that too.

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
with an email when it starts, a week before it goes and when it goes. Point a domain of
your own at the box before then. Paying again turns everything back on.

**Release from ShipTiffin** (in your account) ends the subscription at once, removes the
address and any key we hold, and leaves the server exactly as it is: an ordinary Tiffin box
of yours, which goes on installing updates by itself.

## Abuse

The servers are in customers' own Hetzner accounts; the `shiptiffin.app` addresses are
ours. Report one at shiptiffin.com/abuse or to abuse@shiptiffin.com. ShipTiffin removes an
address used for phishing or malware (the box's owner is told why); the server is untouched.
