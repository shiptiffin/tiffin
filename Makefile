# Tiffin build tasks.

MODULE   := github.com/btahir/tiffin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
# The commit's date, not the build's: the same commit builds the same bytes.
DATE     ?= $(shell TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -buildid= \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)
GOFLAGS_BUILD := -trimpath -ldflags '$(LDFLAGS)'

# Optional command prefix for heavy jobs (e2e), e.g. a script that limits how
# many VM-booting runs share one machine: `make e2e HEAVY=path/to/limiter.sh`.
# Empty by default: the job runs directly.
HEAVY ?=

RELEASE_TARGETS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64

# Release manifest settings (make release-sign): see cmd/tiffin-release.
CHANNELS     ?=
MIN_VERSION  ?=
ROLLOUT      ?= 100
EDGE_RESTART ?= false
NOTES        ?=
BASE_URL     ?=
RELEASE_KEY  ?=

.PHONY: build release release-sign test lint golden-update e2e ci clean auth-engine dashboard sdk

# The web dashboard, built into internal/dashboard/dist and embedded in the
# binary. The build output is committed so `go build` works without Bun.
dashboard:
	cd apps/dashboard && bun install && bun run build

# @shiptiffin/sdk, built from packages/sdk into internal/sdkpkg/files and
# embedded: `tiffin sdk add` vendors it into apps that don't install it from npm.
# The output is committed, so without Bun the build uses what is there.
sdk:
	@if command -v bun >/dev/null 2>&1; then bun scripts/sdk-pack.ts; \
	else echo "bun not found: using the committed SDK build in internal/sdkpkg/files"; fi

build: sdk
	@mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o bin/tiffin ./cmd/tiffin

# Reproducible builds for every target (trimmed paths, no build ID, the
# commit's date, the committed SDK build), named as `tiffin up` looks for them:
# dist/tiffin-<os>-<arch>.
release:
	@rm -rf dist && mkdir -p dist
	@set -e; for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		out="dist/tiffin-$${os}-$${arch}"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_BUILD) -o "$$out" ./cmd/tiffin; \
	done
	@cd dist && (command -v sha256sum >/dev/null 2>&1 && sha256sum tiffin-* || shasum -a 256 tiffin-*) > checksums.txt
	@cat dist/checksums.txt

# The signed manifest per channel (dist/<channel>/manifest.json and
# .minisig) for boxes' automatic updates. The key comes from
# $$TIFFIN_RELEASE_KEY or RELEASE_KEY=<file>, never from the repository.
release-sign:
	go run ./cmd/tiffin-release manifest -dist dist -version $(VERSION) -channels "$(CHANNELS)" -min-version "$(MIN_VERSION)" \
		-rollout $(ROLLOUT) -edge-restart=$(EDGE_RESTART) -notes "$(NOTES)" -base-url "$(BASE_URL)" -key "$(RELEASE_KEY)"

test:
	go test ./...
	@if [ -f packages/package.json ] || ls packages/*/package.json >/dev/null 2>&1; then \
		for d in packages/*/; do if [ -n "$$(find $$d -name '*.test.*' -not -path '*/node_modules/*' | head -1)" ]; then (cd $$d && bun test) || exit 1; fi; done; \
	else echo "packages: no JS packages yet, skipping bun test"; fi

lint:
	@out="$$(gofmt -l $$(git ls-files '*.go' 2>/dev/null; git ls-files --others --exclude-standard '*.go' 2>/dev/null))"; \
		if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi
	go vet -tags e2e ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest -tags e2e ./...

# Bundle the auth engine (packages/auth-engine) for go:embed. The bundle is
# committed, like the dashboard build, so plain `go build` works without Bun.
auth-engine:
	bun build packages/auth-engine/src/main.ts --target=bun --minify --outfile dist/tiffin-auth.js
	gzip -9n -c dist/tiffin-auth.js > internal/mod/auth/engine/tiffin-auth.js.gz
	@ls -l internal/mod/auth/engine/tiffin-auth.js.gz

# Regenerate golden files. Test packages read the UPDATE_GOLDEN env var.
golden-update:
	UPDATE_GOLDEN=1 go test ./... -count=1

e2e:
	$(HEAVY) go test -tags e2e ./e2e/... -timeout 75m -count=1 -v

# Same as scripts/ci.sh: lint + test + a release dry run.
ci:
	./scripts/ci.sh

# The routine update pass: vulnerability scan + tested Renovate merges.
maintain:
	./scripts/maintain.sh $(ARGS)

clean:
	rm -rf bin dist
