.PHONY: help release release-roll release-roll-commit web typecheck test

# Repository helpers. The release flow lives in hack/release.sh — the
# Makefile is just an ergonomic shim so common invocations are one
# token long.

help:
	@echo "kuso make targets:"
	@echo "  make release VERSION=v0.3.5"
	@echo "      bump version files, build web, cross-build amd64 image, push to GHCR"
	@echo "  make release-roll VERSION=v0.3.5"
	@echo "      everything in 'release', then ssh + kubectl rollout"
	@echo "  make release-roll-commit VERSION=v0.3.5"
	@echo "      everything in 'release-roll', then git commit the version-file changes"
	@echo ""
	@echo "  make typecheck    # tsc on web/"
	@echo "  make web          # pnpm --dir web build"
	@echo "  make test         # go test ./... in server-go"

VERSION ?=

release:
	@if [ -z "$(VERSION)" ]; then echo "usage: make release VERSION=vX.Y.Z" >&2; exit 2; fi
	@./hack/release.sh $(VERSION)

release-roll:
	@if [ -z "$(VERSION)" ]; then echo "usage: make release-roll VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_ROLL=1 ./hack/release.sh $(VERSION)

release-roll-commit:
	@if [ -z "$(VERSION)" ]; then echo "usage: make release-roll-commit VERSION=vX.Y.Z" >&2; exit 2; fi
	@KUSO_RELEASE_ROLL=1 KUSO_RELEASE_COMMIT=1 ./hack/release.sh $(VERSION)

web:
	@cd web && pnpm build

typecheck:
	@cd web && pnpm typecheck

test:
	@cd server-go && go test ./...

# CLI builds — writes kuso-{darwin,linux}-{amd64,arm64} into dist/.
# Used by the GitHub release workflow; run locally with `make cli`.
.PHONY: cli cli-darwin-arm64 cli-darwin-amd64 cli-linux-amd64 cli-linux-arm64
cli: cli-darwin-arm64 cli-darwin-amd64 cli-linux-amd64 cli-linux-arm64
cli-darwin-arm64:
	@mkdir -p dist
	@cd cli && GOOS=darwin GOARCH=arm64 go build -o ../dist/kuso-darwin-arm64 ./cmd
cli-darwin-amd64:
	@mkdir -p dist
	@cd cli && GOOS=darwin GOARCH=amd64 go build -o ../dist/kuso-darwin-amd64 ./cmd
cli-linux-amd64:
	@mkdir -p dist
	@cd cli && GOOS=linux GOARCH=amd64 go build -o ../dist/kuso-linux-amd64 ./cmd
cli-linux-arm64:
	@mkdir -p dist
	@cd cli && GOOS=linux GOARCH=arm64 go build -o ../dist/kuso-linux-arm64 ./cmd
