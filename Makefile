.PHONY: test race verify-plan verify-focused verify-candidate verify-final docs-check lint build build-go snapshot release-dry integration integration-contracts integration-live provider-smoke development-eval clean help install-local build-clients cross web web-stub web-sync web-check web-dev

VERIFY_CMD := uv run --quiet --project . python -m tests.eval.juex_eval verify
PLAN_CMD := uv run --quiet --project . python -m tests.eval.juex_eval plan
VERIFY_RACE_FLAG := $(if $(filter 1,$(RACE)),--race,)
VERIFY_WEB_FLAG := $(if $(filter 1,$(WEB)),--web,)
VERIFY_COMPACTION_FLAG := $(if $(filter 1,$(COMPACTION)),--compaction,)
VERIFY_BASE_FLAG := $(if $(BASE),--base $(BASE),)
VERIFY_EXPLAIN_FLAG := $(if $(filter 1,$(EXPLAIN)),--explain,)
VERIFY_PLANNED_FLAG := $(if $(filter 1,$(PLANNED)),--planned,)

web:
	cd frontend && pnpm install && pnpm build
	$(MAKE) web-sync

web-stub:
	mkdir -p internal/entrypoints/webassets/dist
	@test -f internal/entrypoints/webassets/dist/index.html || printf '%s\n' '<!doctype html><html><body></body></html>' > internal/entrypoints/webassets/dist/index.html

web-sync:
	rm -rf internal/entrypoints/webassets/dist
	mkdir -p internal/entrypoints/webassets/dist
	cp -R frontend/dist/. internal/entrypoints/webassets/dist/

web-check:
	cd frontend && pnpm install --frozen-lockfile
	cd frontend && pnpm exec tsc -b
	cd frontend && pnpm test
	cd frontend && pnpm lint
	cd frontend && pnpm build
	cd frontend && pnpm test:browser
	$(MAKE) web-sync

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

LDFLAGS := -X github.com/juex-ai/juex/internal/foundation/version.Version=$(VERSION) \
           -X github.com/juex-ai/juex/internal/foundation/version.Commit=$(COMMIT) \
           -X github.com/juex-ai/juex/internal/foundation/version.BuildTime=$(BUILDTIME)

help:
	@echo "Targets:"
	@echo "  verify-plan [TIER=focused] [BASE=...] [EXPLAIN=1]  deterministic Git-diff gate plan"
	@echo "  verify-focused PKGS=... or PLANNED=1 [BASE=...]  explicit scope or dirty diff plan"
	@echo "  verify-candidate [RACE=1] [WEB=1] [BASE=...]  planned commit-bound deterministic PR gate"
	@echo "  verify-final [RACE=1] [WEB=1] [COMPACTION=1] [BASE=...]  reuse candidate and run planned live gates"
	@echo "  test          go test ./... (caller environment)"
	@echo "  race          go test ./... -race (caller environment)"
	@echo "  lint          golangci-lint run"
	@echo "  build         build all clients and services with embedded Web and version metadata"
	@echo "  build-go      build all clients and services from existing embedded frontend assets"
	@echo "  web-stub      prepare lightweight embedded assets for Go-only checks"
	@echo "  install-local install both Linux/macOS clients under ~/.local/bin"
	@echo "  cross         build Linux/macOS client archives with GoReleaser"
	@echo "  snapshot      goreleaser cross-platform snapshot (dist/)"
	@echo "  release-dry   goreleaser release without publishing"
	@echo "  integration   managed platform with explicit live provider config"
	@echo "  provider-smoke live provider:model smoke selected from provider config"
	@echo "  development-eval standard post-development validation record"
	@echo "  docs-check    test and enforce bilingual Markdown pairing and links"
	@echo "  web-check     install, type-check, test, lint, and build the frontend"
	@echo "  clean         remove dist/"

test:
	go test ./...

race:
	go test ./... -race -count=1

verify-plan:
	$(PLAN_CMD) --tier $(or $(TIER),focused) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG)

verify-focused:
	$(VERIFY_CMD) focused $(strip $(VERIFY_PLANNED_FLAG) $(PKGS) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG))

verify-candidate:
	$(VERIFY_CMD) candidate $(VERIFY_RACE_FLAG) $(VERIFY_WEB_FLAG) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG)

verify-final:
	$(VERIFY_CMD) final $(VERIFY_RACE_FLAG) $(VERIFY_WEB_FLAG) $(VERIFY_COMPACTION_FLAG) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG)

docs-check:
	uv run --quiet --project . python -m unittest scripts.test_check_bilingual_docs
	uv run --quiet --project . python scripts/check_bilingual_docs.py

lint:
	golangci-lint run

build: web
	$(MAKE) build-go

CLIENTS := juex juex-executor
SERVICES := juex-management juex-runtime juex-execution juex-memory juex-calendar juex-guest juex-service-log juex-migrate

build-clients:
	mkdir -p dist
	@for binary in $(CLIENTS); do CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$binary ./cmd/$$binary || exit $$?; done

build-go: build-clients
	@for binary in $(SERVICES); do CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$binary ./cmd/$$binary || exit $$?; done

install-local:
	uv run --quiet --project . python scripts/install-local.py

cross:
	goreleaser release --snapshot --clean

snapshot:
	goreleaser release --snapshot --clean

release-dry:
	goreleaser release --skip=publish --clean

integration: integration-contracts integration-live

integration-contracts:
	@test -n "$$JUEX_TEST_POSTGRES_URL" || (echo 'JUEX_TEST_POSTGRES_URL is required' >&2; exit 1)
	go test -race -tags=postgres ./tests/e2e -count=1 -timeout=20m

integration-live:
	uv run --quiet --project . python -m tests.eval.juex_eval integration

provider-smoke: build
	bash tests/eval/provider_model_smoke.sh

development-eval:
	bash tests/eval/development_eval.sh

clean:
	rm -rf dist
