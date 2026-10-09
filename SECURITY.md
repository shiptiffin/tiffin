# Security

## Reporting a problem

Please don't open a public issue for a security problem. Email **hello@shiptiffin.com**
with "Security" in the subject, or use **Report a vulnerability** on this repository's
Security tab.

Tell us what you found, how to reproduce it, which version (`tiffin version`) and what
someone could do with it. We aim to reply within a few working days, keep you posted while
we fix it, and credit you in the release notes if you'd like. Please give us a reasonable time to ship
a fix before you talk about it publicly.

There is no bug bounty.

## Supported versions

Only the latest release gets security fixes. Boxes update themselves to the newest signed
release of their channel (`tiffin update status` shows yours), so a fix reaches them without
anyone running a command, unless automatic updates were turned off.

## In scope

- the `tiffin` binary: the CLI, the MCP server, the box's API, dashboard and edge
- isolation between projects and apps on one box
- the install script and the release signing
- shiptiffin.com and the managed setup, including how a Hetzner API key is handled

Known design limits are listed in [docs/guide/limits.md](docs/guide/limits.md), and the
security model in [docs/guide/security.md](docs/guide/security.md). A report that shows one
of them is worse than written there is welcome too.

Phishing or malware on a `shiptiffin.app` address is abuse, not a vulnerability: report it at
[shiptiffin.com/abuse](https://shiptiffin.com/abuse).
