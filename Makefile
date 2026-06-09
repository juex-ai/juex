.PHONY: test lint build snapshot release-dry integration provider-smoke development-eval clean help install-local cross web web-dev

web:
	cd frontend && pnpm install && pnpm build
	rm -rf internal/web/dist
	mkdir -p internal/web/dist
	cp -R frontend/dist/. internal/web/dist/

web-dev:
	cd frontend && pnpm dev

# Read VERSION from CLI_CONFIG (single source of truth). The git describe
# output is preferred when available (carries dirty / commit suffix), else
# fall back to the bare CLI_CONFIG value (suffixed -dev).
CLI_CONFIG_VERSION := $(shell awk -F= '/^VERSION=/{print $$2}' CLI_CONFIG)
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo $(CLI_CONFIG_VERSION)-dev)
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDTIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

DIST_BIN := dist/juex

LDFLAGS := -X github.com/juex-ai/juex/internal/version.Version=$(VERSION) \
           -X github.com/juex-ai/juex/internal/version.Commit=$(COMMIT) \
           -X github.com/juex-ai/juex/internal/version.BuildTime=$(BUILDTIME)

help:
	@echo "Targets:"
	@echo "  test          go test ./..."
	@echo "  lint          golangci-lint run"
	@echo "  build         produce $(DIST_BIN) with embedded version metadata"
	@echo "  install-local install ~/.local/bin/juex (builds via dist/)"
	@echo "  cross         build all 6 platform archives in dist/ (no goreleaser)"
	@echo "  snapshot      goreleaser cross-platform snapshot (dist/)"
	@echo "  release-dry   goreleaser release without publishing"
	@echo "  integration   go test -tags=integration ./tests/e2e/..."
	@echo "  provider-smoke live rotating provider/model smoke from tests/eval/live-models.yaml"
	@echo "  development-eval standard post-development validation record"
	@echo "  clean         remove dist/"

test:
	go test ./... -count=1

lint:
	golangci-lint run

build: web
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_BIN) ./cmd/juex

install-local:
	./scripts/install-local.sh

cross:
	./scripts/build.sh

snapshot:
	goreleaser release --snapshot --clean

release-dry:
	goreleaser release --skip=publish --clean

integration:
	go test -tags=integration ./tests/e2e/... -count=1

provider-smoke: build
	bash tests/eval/provider_model_smoke.sh --juex $(DIST_BIN)

development-eval:
	bash tests/eval/development_eval.sh

clean:
	rm -rf dist
