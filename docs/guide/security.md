# Security model

Plainly, so you can decide what to trust it with.

- **The box is the boundary.** Tiffin runs as root on its own machine and manages
  system services. Apps run in containers. Do not share a box with people you don't trust.
- **HTTPS everywhere.** Locally the box has its own certificate authority, created on
  the box and never shared. `tiffin trust` adds it to your Mac's keychain.
- **Only HTTPS leaves a local box,** and only to `127.0.0.1:8443` on your Mac. Postgres,
  Valkey and the rest are not reachable from outside the VM.
- **Tokens** are random 200-bit secrets; only their SHA-256 is stored. Agent tokens
  expire, can't mint more power than they hold, and are revoked with everything they minted.
- **Secrets** (env vars) are encrypted with the box's own age key and are never shown
  after you set them.
- **Approvals** for risky plans need a person's passkey, are bound to the exact plan
  hash, single-use and expire after a day.
- **Every change and security event is logged** (changes, tokens, approvals, secrets).
- **Known gaps:** a person or agent with shell access to your Mac can read your local
  owner token in `~/.tiffin`. Backups stay on the box until off-site storage arrives.
