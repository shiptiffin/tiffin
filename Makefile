# Tiffin build tasks. Heavy jobs (release, e2e) go through research/heavy.sh
# so at most two run machine-wide.

MODULE   := github.com/btahir/tiffin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)
GOFLAGS_BUILD := -trimpath -ldflags '$(LDFLAGS)'

# Override HEAVY= to run without the limiter (CI on a clean machine).
HEAVY ?= $(abspath $(CURDIR)/../research/heavy.sh)

RELEASE_TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64 windows/arm64

.PHONY: build release test lint golden-update e2e ci clean dashboard

# The web dashboard, built into internal/dashboard/dist and embedded in the
# binary. The build output is committed so `go build` works without Bun.
dashboard:
	cd apps/dashboard && bun install && bun run build

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o bin/tiffin ./cmd/tiffin

release:
	@rm -rf dist && mkdir -p dist
	@set -e; for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
		out="dist/tiffin_$${os}_$${arch}$$ext"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_BUILD) -o "$$out" ./cmd/tiffin; \
	done
	@cd dist && (command -v sha256sum >/dev/null 2>&1 && sha256sum tiffin_* || shasum -a 256 tiffin_*) > checksums.txt
	@cat dist/checksums.txt

test:
	go test ./...
	@if [ -f packages/package.json ] || ls packages/*/package.json >/dev/null 2>&1; then \
		cd packages && bun test; \
	else echo "packages: no JS packages yet, skipping bun test"; fi

lint:
	@out="$$(gofmt -l $$(git ls-files '*.go' 2>/dev/null; git ls-files --others --exclude-standard '*.go' 2>/dev/null))"; \
		if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi
	go vet -tags e2e ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest -tags e2e ./...

# Regenerate golden files. Test packages read the UPDATE_GOLDEN env var.
golden-update:
	UPDATE_GOLDEN=1 go test ./... -count=1

e2e:
	$(HEAVY) go test -tags e2e ./e2e/... -timeout 20m -count=1 -v

# Same as scripts/ci.sh: lint + test + a release dry run.
ci:
	./scripts/ci.sh

clean:
	rm -rf bin dist
