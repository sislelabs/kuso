.PHONY: help ship roll dry-run web typecheck test

# Repository helpers. The release flow lives in hack/release.sh — the
# Makefile is just an ergonomic shim so common invocations are one
# token long.

help:
	@echo "kuso make targets:"
	@echo ""
	@echo "  RELEASE (use these — they're the supported entry points):"
	@echo ""
	@echo "  make dry-run VERSION=v0.7.12"
	@echo "      preview what 'make ship' would do — no docker push,"
	@echo "      no gh release, no git push. ALWAYS run this first."
	@echo ""
	@echo "  make ship VERSION=v0.7.12"
	@echo "      RELEASE A NEW VERSION: bump version files, build web,"
	@echo "      push kuso-server image to ghcr, detect operator/ changes"
	@echo "      (auto-build operator image), cross-build CLI binaries,"
	@echo "      regenerate CHANGELOG, cut GH release with all assets,"
	@echo "      git commit + tag + push."
	@echo ""
	@echo "      Does NOT roll any cluster. Each kuso install pulls"
	@echo "      itself forward via /api/system/update — instances poll"
	@echo "      GH for new releases on their own."
	@echo ""
	@echo "  Live instances upgrade themselves:"
	@echo "      kuso upgrade                    # update to latest"
	@echo "      kuso upgrade --version vX.Y.Z   # pin to a tag"
	@echo "      (or click Update in the dashboard)"
	@echo ""
	@echo "  DEV:"
	@echo "  make typecheck    # tsc on web/"
	@echo "  make web          # pnpm --dir web build"
	@echo "  make test         # go test ./... in server-go"
	@echo ""
	@echo "  Local-only escape hatch (you almost never want this):"
	@echo "  make local-roll VERSION=vX.Y.Z"
	@echo "      ssh into the configured KUSO_RELEASE_HOST and"
	@echo "      'kubectl set image' to that already-released tag."
	@echo "      For your dev test cluster only — production should"
	@echo "      always self-update."

VERSION ?=

# `make ship` builds artifacts + cuts a GH release. It does NOT touch
# any kuso instance; instances poll the GH releases endpoint and pull
# themselves forward via the in-built updater. That keeps the release
# flow stateless w.r.t. cluster topology — same path your dev box,
# customer A's box, and customer B's box take.
ship:
	@if [ -z "$(VERSION)" ]; then echo "usage: make ship VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh $(VERSION)

# `make dry-run` mirrors `make ship` but with --dry-run, so you can
# preview the side effects of a release before paying the docker-push
# + git-push cost. Recommended before every real release.
dry-run:
	@if [ -z "$(VERSION)" ]; then echo "usage: make dry-run VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_ALLOW_DIRTY=1 KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh --dry-run $(VERSION)

# `make local-roll` is the dev-only escape hatch: ssh into the test
# cluster KUSO_RELEASE_HOST=kuso.sislelabs.com and `kubectl set image`
# to a tag that's already on ghcr. Almost no one should run this —
# production clusters self-update. Useful only when iterating on the
# updater itself (where the in-cluster path is what you're debugging).
local-roll:
	@if [ -z "$(VERSION)" ]; then echo "usage: make local-roll VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_ROLL=1 KUSO_RELEASE_SKIP_BUILD=1 ./hack/release.sh $(VERSION)

# Deprecated targets — they used to ssh into a cluster as part of
# release. That's now `make local-roll` only. Removing in v0.8.
.PHONY: release release-roll release-roll-commit roll
release release-roll release-roll-commit roll:
	@echo "==> 'make $@' is deprecated." >&2
	@echo "==> Releases ('make ship') no longer roll any cluster — instances self-update." >&2
	@echo "==> If you really want to ssh+kubectl from your laptop, use 'make local-roll'." >&2
	@exit 2

web:
	@cd web && pnpm build

typecheck:
	@cd web && pnpm typecheck

test:
	@cd server-go && go test ./...

# verify: lightweight CI gate. Runs typechecks + tests + a CLI/API
# parity grep that catches new HTTP routes added without a matching
# CLI command. Not airtight — it's a heuristic — but it surfaces the
# common "added an endpoint, forgot the CLI" mistake before review.
.PHONY: verify verify-parity update-goldens
verify: typecheck test verify-parity

verify-parity:
	@bash hack/verify-parity.sh

update-goldens:
	@cd server-go && KUSO_UPDATE_GOLDENS=1 go test ./internal/kube/ -run TestCRDSchema_GoldenStable

# CLI builds — writes kuso-{darwin,linux}-{amd64,arm64} into dist/.
# Used by the release flow (hack/release.sh attaches them as GitHub
# release assets so the install-cli.sh one-liner works); run locally
# with `make cli`. CLI_VERSION is injected via ldflags so `kuso version`
# reports the tag instead of v0.1.0-dev.
CLI_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
CLI_LDFLAGS = -X kuso/cmd/kusoCli/version.ldflagsVersion=$(CLI_VERSION)

.PHONY: cli cli-darwin-arm64 cli-darwin-amd64 cli-linux-amd64 cli-linux-arm64
cli: cli-darwin-arm64 cli-darwin-amd64 cli-linux-amd64 cli-linux-arm64
cli-darwin-arm64:
	@mkdir -p dist
	@cd cli && GOOS=darwin GOARCH=arm64 go build -ldflags="$(CLI_LDFLAGS)" -o ../dist/kuso-darwin-arm64 ./cmd
cli-darwin-amd64:
	@mkdir -p dist
	@cd cli && GOOS=darwin GOARCH=amd64 go build -ldflags="$(CLI_LDFLAGS)" -o ../dist/kuso-darwin-amd64 ./cmd
cli-linux-amd64:
	@mkdir -p dist
	@cd cli && GOOS=linux GOARCH=amd64 go build -ldflags="$(CLI_LDFLAGS)" -o ../dist/kuso-linux-amd64 ./cmd
cli-linux-arm64:
	@mkdir -p dist
	@cd cli && GOOS=linux GOARCH=arm64 go build -ldflags="$(CLI_LDFLAGS)" -o ../dist/kuso-linux-arm64 ./cmd

# Sync hack/install*.sh into the embed dir consumed by server-go.
# server-go embeds these to serve them at /install.sh + /install-cli.sh,
# but Go's go:embed can't reach into ../../hack/. So this target keeps
# the embed copies in lock-step with the canonical sources. release.sh
# runs this before each server build.
.PHONY: sync-install-scripts
sync-install-scripts:
	@cp hack/install.sh server-go/internal/installscripts/scripts/install.sh
	@cp hack/install-cli.sh server-go/internal/installscripts/scripts/install-cli.sh
	@echo "synced install scripts → server-go/internal/installscripts/scripts/"

# Backup image — alpine + aws-cli + postgresql-client. Referenced by
# the per-addon backup CronJob template. Cross-build amd64 by default
# since most kuso clusters run on amd64; tag includes the version
# suffix so old CronJobs don't get re-pulled into a different image.
.PHONY: backup-image
BACKUP_VERSION ?= v0.5.0
backup-image:
	@docker buildx build --platform linux/amd64 --push \
		-t ghcr.io/sislelabs/kuso-backup:$(BACKUP_VERSION) \
		-t ghcr.io/sislelabs/kuso-backup:latest \
		-f build/backup/Dockerfile build/backup

# Updater image — alpine + kubectl + curl + jq. Each release ships
# its own updater so the script that handles a v0.5.0 upgrade always
# matches the v0.5.0 manifest's expectations. The :latest tag is
# overwritten on every release so older instances upgrading to NEW
# pull the right script even when the cached :latest is stale.
.PHONY: updater-image
UPDATER_VERSION ?= v0.4.2
updater-image:
	@docker buildx build --platform linux/amd64 --push \
		-t ghcr.io/sislelabs/kuso-updater:$(UPDATER_VERSION) \
		-t ghcr.io/sislelabs/kuso-updater:latest \
		-f build/updater/Dockerfile build/updater
