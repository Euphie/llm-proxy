SHELL := /bin/bash
.SHELLFLAGS := -o pipefail -c

BIN     := llm-proxy
CMD     := ./cmd/llm-proxy
IMAGE   := llm-proxy
DOCS_SITE_INPUTS := app assets build components content data diagrams lib public scripts tests types worker eslint.config.mjs next.config.ts package-lock.json package.json playwright.config.ts postcss.config.mjs tsconfig.json vite.config.ts
DOCS_ARCHIVE_INPUTS := Makefile docs/intelligent-routing.md $(addprefix docs-site/,$(DOCS_SITE_INPUTS))

.PHONY: build run test vet test-e2e docker-build docker-up docker-down clean update-model-catalog docs-verify docs-e2e docs-preview

## build: compile binary to ./bin/llm-proxy
build:
	go build -o bin/$(BIN) $(CMD)

## run: run with local runtime data in ./data
run: build
	DATA_DIR=./data ./bin/$(BIN)

## vet: run go vet
vet:
	go vet ./...

## test: run all tests
test:
	go test -race ./...

## test-ui: run admin UI module tests in Docker
.PHONY: test-ui
test-ui:
	docker run --rm \
		-v "$(CURDIR)":/src:ro -w /src \
		node:24-alpine \
		node --test \
			scripts/risk-policy-defaults.test.mjs \
			scripts/model-catalog-lib.test.mjs \
			scripts/model-catalog-update-lib.test.mjs \
			internal/admin/ui/static/visual.test.mjs \
			internal/admin/ui/static/model-catalog.test.mjs \
			internal/admin/ui/static/auth.test.mjs \
			internal/admin/ui/static/profiles.test.mjs \
			internal/admin/ui/static/generator.test.mjs \
			internal/admin/ui/static/stats.test.mjs

## update-model-catalog: refresh the pinned Models.dev browser snapshot
update-model-catalog:
	docker run --rm \
		-v "$(CURDIR)":/src -w /src \
		node:24-alpine \
		node scripts/update-model-catalog.mjs

## test-e2e: run the isolated Chromium admin workflow
test-e2e:
	@set -e; \
	trap 'docker compose -f docker-compose.yml -f docker-compose.e2e.yml -p llm-proxy-e2e down --volumes' EXIT; \
	docker compose \
		-f docker-compose.yml \
		-f docker-compose.e2e.yml \
		-p llm-proxy-e2e \
		up --build --abort-on-container-exit --exit-code-from e2e

## docs-verify: verify the intelligent-routing documentation in an isolated Node container
docs-verify:
	COPYFILE_DISABLE=1 tar --no-xattrs --exclude='._*' --exclude='.DS_Store' -C "$(CURDIR)" -cf - $(DOCS_ARCHIVE_INPUTS) | \
		docker run --rm -i \
		node:22-bookworm \
		sh -lc 'mkdir -p /tmp/repo && tar -xf - -C /tmp/repo && cd /tmp/repo/docs-site && npm ci && npm run verify'

## docs-e2e: run the production documentation browser suite in pinned Chromium
docs-e2e:
	COPYFILE_DISABLE=1 tar --no-xattrs --exclude='._*' --exclude='.DS_Store' -C "$(CURDIR)" -cf - $(DOCS_ARCHIVE_INPUTS) | \
		docker run --rm -i --ipc=host \
		mcr.microsoft.com/playwright:v1.62.0-noble \
		sh -lc 'mkdir -p /tmp/repo && tar -xf - -C /tmp/repo && cd /tmp/repo/docs-site && npm ci && npm run test:e2e'

## docs-preview: run the documentation preview from an isolated temporary copy
docs-preview:
	COPYFILE_DISABLE=1 tar --no-xattrs --exclude='._*' --exclude='.DS_Store' -C "$(CURDIR)" -cf - $(DOCS_ARCHIVE_INPUTS) | \
		docker run --rm -i -p 3000:3000 \
		node:22-bookworm \
		sh -lc 'mkdir -p /tmp/repo && tar -xf - -C /tmp/repo && cd /tmp/repo/docs-site && npm ci && npm run dev -- --host 0.0.0.0'

## docker-build: build Docker image
docker-build:
	docker build -t $(IMAGE):latest .

## docker-up: start via docker compose
docker-up:
	docker compose up -d

## docker-down: stop via docker compose
docker-down:
	docker compose down

## clean: remove build artifacts
clean:
	rm -rf bin/

## help: show this message
help:
	@grep -E '^## ' Makefile | sed 's/^## /  /'
