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
	@echo "      no kubectl, no gh release, no git push. ALWAYS run this"
	@echo "      first before a real release."
	@echo ""
	@echo "  make ship VERSION=v0.7.12"
	@echo "      ONE-COMMAND RELEASE: bump version files, build web,"
	@echo "      push kuso-server image, roll deploy, detect operator/"
	@echo "      changes (auto-build operator image + apply CRDs +"
	@echo "      roll operator), cross-build CLI binaries, cut GH"
	@echo "      release with all assets, git commit + tag + push."
	@echo ""
	@echo "  make roll VERSION=v0.7.12"
	@echo "      roll an ALREADY-RELEASED version onto the live cluster."
	@echo "      Useful when CI built the artifacts on tag-push and you"
	@echo "      just need to point production at them. No image rebuild."
	@echo ""
	@echo "  DEV:"
	@echo "  make typecheck    # tsc on web/"
	@echo "  make web          # pnpm --dir web build"
	@echo "  make test         # go test ./... in server-go"
	@echo ""
	@echo "  Deprecated: make release, make release-roll, make release-roll-commit"
	@echo "    All print an error pointing at ship/roll/dry-run. Removed in v0.8."

VERSION ?=

# `make ship` is the one-command release flow most callers want.
# Auto-detects operator/ changes and rolls both images + CRDs +
# commit in a single invocation. KUSO_RELEASE_OPERATOR=0 forces
# skip-operator (rare); KUSO_RELEASE_OPERATOR=1 forces always-build.
ship:
	@if [ -z "$(VERSION)" ]; then echo "usage: make ship VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_ROLL=1 KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh $(VERSION)

# `make roll` is the rollout-only path: image already exists at
# ghcr.io/sislelabs/kuso-server-go:VERSION (typically because CI built
# it on tag-push), and you just want to point the live cluster at it.
# No image rebuild, no version-file bumps, no GH release. Faster than
# `make ship` for that case (~30s vs ~5min).
roll:
	@if [ -z "$(VERSION)" ]; then echo "usage: make roll VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_ROLL=1 KUSO_RELEASE_SKIP_BUILD=1 ./hack/release.sh $(VERSION)

# `make dry-run` mirrors `make ship` but with --dry-run, so you can
# preview the side effects of a release before paying the docker-push
# + git-push cost. Recommended before every real release.
dry-run:
	@if [ -z "$(VERSION)" ]; then echo "usage: make dry-run VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_ALLOW_DIRTY=1 KUSO_RELEASE_ROLL=1 KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh --dry-run $(VERSION)

# Deprecated targets. Kept around for muscle memory; they all redirect
# to the new entry points + print a deprecation warning. Removing
# these in v0.8.
.PHONY: release release-roll release-roll-commit
release release-roll release-roll-commit:
	@echo "==> 'make $@' is deprecated. Use 'make ship', 'make roll', or 'make dry-run' instead. See 'make help'." >&2
	@echo "==> Falling through to 'make ship' for now…" >&2
	@$(MAKE) ship VERSION=$(VERSION)

web:
	@cd web && pnpm build

typecheck:
	@cd web && pnpm typecheck

test:
	@cd server-go && go test ./...

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
BACKUP_VERSION ?= v0.4.0
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
