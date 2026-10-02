# Tiffin

**Your app in a box.**

Tiffin gives your whole app stack one small Linux box to live in: the web app, the database, background jobs, file storage, email and the dashboard that watches over them. The box is run by agents. You say what you want, they plan the change, show you the diff, and apply it when you approve.

It ships as one Go binary, `tiffin`, with a CLI, an MCP server and a typed SDK that all come from the same API spec.

> **Status: pre-alpha (M0).** The foundations are being laid and nothing here is ready to run your app yet. Expect breaking changes every day.

## Is this for you?

Tiffin is for hobby projects and experiments: the side project you want running tonight, the thing you are trying out with an agent, the weekend app. It is not hardened for production traffic, it has had no security audit, and it comes with no promises about your data. Keep backups of anything you care about.

## How it works (the short version)

- One command creates a Linux box (a local VM on your Mac to start with) and installs everything over SSH.
- Every change is a plan first. Dry-run is the default, and `--confirm <hash>` applies it.
- The CLI prints JSON when it is not on a terminal, never prompts, and uses clear exit codes, so agents can drive it.
- Your project lives in one file, `tiffin.config.ts`.

## Developing

You need Go 1.27+, [Bun](https://bun.sh) and, for the end-to-end tests, [Lima](https://lima-vm.io) on macOS.

```sh
make test      # go test ./... (and bun test for the JS packages)
make build     # host binary at bin/tiffin
make e2e       # boots a throwaway Lima VM and checks the box (slow, first run downloads Ubuntu)
```

More targets:

```sh
make lint      # gofmt, go vet, staticcheck
make release   # cross-compile six binaries into dist/ with sha256 checksums
make ci        # lint + test + release dry run (same as scripts/ci.sh)
```

The e2e tests clean up after themselves. If one is interrupted, the next run removes leftover `tiffin-e2e-*` VMs older than two hours. Set `TIFFIN_E2E_KEEP=1` to keep a VM around for poking at.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
