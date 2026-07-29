BIN     := llm-proxy
CMD     := ./cmd/llm-proxy
IMAGE   := llm-proxy

.PHONY: build run test vet test-e2e docker-build docker-up docker-down clean

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
			internal/admin/ui/static/auth.test.mjs \
			internal/admin/ui/static/profiles.test.mjs \
			internal/admin/ui/static/generator.test.mjs \
			internal/admin/ui/static/stats.test.mjs

## test-e2e: run the isolated Chromium admin workflow
test-e2e:
	@set -e; \
	trap 'docker compose -f docker-compose.yml -f docker-compose.e2e.yml -p llm-proxy-e2e down --volumes' EXIT; \
	docker compose \
		-f docker-compose.yml \
		-f docker-compose.e2e.yml \
		-p llm-proxy-e2e \
		up --build --abort-on-container-exit --exit-code-from e2e

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
