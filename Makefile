.PHONY: test race verify-plan verify-focused verify-candidate verify-final lint build build-go snapshot release-dry integration provider-smoke development-eval clean help install-local cross web web-stub web-sync web-check web-dev ripgrep

VERIFY_CMD := uv run --quiet --project . python -m tests.eval.juex_eval verify
PLAN_CMD := uv run --quiet --project . python -m tests.eval.juex_eval plan
VERIFY_RACE_FLAG := $(if $(filter 1,$(RACE)),--race,)
VERIFY_WEB_FLAG := $(if $(filter 1,$(WEB)),--web,)
VERIFY_COMPACTION_FLAG := $(if $(filter 1,$(COMPACTION)),--compaction,)
VERIFY_BASE_FLAG := $(if $(BASE),--base $(BASE),)
VERIFY_EXPLAIN_FLAG := $(if $(filter 1,$(EXPLAIN)),--explain,)

web:
	cd frontend && pnpm install && pnpm build
	$(MAKE) web-sync

web-stub:
	mkdir -p internal/web/dist
	@test -f internal/web/dist/index.html || printf '%s\n' '<!doctype html><html><body></body></html>' > internal/web/dist/index.html

web-sync:
	rm -rf internal/web/dist
	mkdir -p internal/web/dist
	cp -R frontend/dist/. internal/web/dist/

web-check:
	cd frontend && pnpm install --frozen-lockfile
	cd frontend && pnpm exec tsc -b
	cd frontend && pnpm test
	cd frontend && pnpm lint
	cd frontend && pnpm build
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

LDFLAGS := -X github.com/juex-ai/juex/internal/version.Version=$(VERSION) \
           -X github.com/juex-ai/juex/internal/version.Commit=$(COMMIT) \
           -X github.com/juex-ai/juex/internal/version.BuildTime=$(BUILDTIME)

help:
	@echo "Targets:"
	@echo "  verify-plan [TIER=focused] [BASE=...] [EXPLAIN=1]  deterministic Git-diff gate plan"
	@echo "  verify-focused [PKGS=...] [BASE=...]  dirty diff plan or explicit isolated package tests"
	@echo "  verify-candidate [RACE=1] [WEB=1] [BASE=...]  planned commit-bound deterministic PR gate"
	@echo "  verify-final [RACE=1] [WEB=1] [COMPACTION=1] [BASE=...]  reuse candidate and run planned live gates"
	@echo "  test          go test ./... (isolated HOME/JUEX_HOME, auto-provisions ripgrep)"
	@echo "  race          go test ./... -race (isolated HOME/JUEX_HOME, auto-provisions ripgrep)"
	@echo "  ripgrep       ensure a resolvable ripgrep and print its path"
	@echo "  lint          golangci-lint run"
	@echo "  build         produce $(DIST_BIN) with embedded version metadata"
	@echo "  build-go      produce $(DIST_BIN) from existing embedded frontend assets"
	@echo "  web-stub      prepare lightweight embedded assets for Go-only checks"
	@echo "  install-local install ~/.local/bin/juex (builds via dist/)"
	@echo "  cross         build all 7 platform archives in dist/ (no goreleaser)"
	@echo "  snapshot      goreleaser cross-platform snapshot (dist/)"
	@echo "  release-dry   goreleaser release without publishing"
	@echo "  integration   isolated runtime with explicit live provider/Codex sources"
	@echo "  provider-smoke live provider:model smoke selected from provider config"
	@echo "  development-eval standard post-development validation record"
	@echo "  web-check     install, type-check, test, lint, and build the frontend"
	@echo "  clean         remove dist/"

test:
	PATH="$$(scripts/ensure-ripgrep.sh):$$PATH" ./scripts/with-test-juex-home.sh go test ./... -count=1

race:
	PATH="$$(scripts/ensure-ripgrep.sh):$$PATH" ./scripts/with-test-juex-home.sh go test ./... -race -count=1

verify-plan:
	$(PLAN_CMD) --tier $(or $(TIER),focused) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG)

verify-focused:
	$(VERIFY_CMD) focused $(PKGS) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG)

verify-candidate:
	$(VERIFY_CMD) candidate $(VERIFY_RACE_FLAG) $(VERIFY_WEB_FLAG) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG)

verify-final:
	$(VERIFY_CMD) final $(VERIFY_RACE_FLAG) $(VERIFY_WEB_FLAG) $(VERIFY_COMPACTION_FLAG) $(VERIFY_BASE_FLAG) $(VERIFY_EXPLAIN_FLAG)

ripgrep:
	@scripts/ensure-ripgrep.sh

lint:
	golangci-lint run

build: web
	$(MAKE) build-go

build-go:
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
	PATH="$$(scripts/ensure-ripgrep.sh):$$PATH" ./scripts/with-test-juex-home.sh --live go test -tags=integration ./tests/e2e/... -count=1 -v

provider-smoke: build
	bash tests/eval/provider_model_smoke.sh --juex $(DIST_BIN)

development-eval:
	bash tests/eval/development_eval.sh

clean:
	rm -rf dist
