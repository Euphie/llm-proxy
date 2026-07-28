BIN     := llm-proxy
CMD     := ./cmd/llm-proxy
IMAGE   := llm-proxy

.PHONY: build run test vet docker-build docker-up docker-down clean

## build: compile binary to ./bin/llm-proxy
build:
	go build -o bin/$(BIN) $(CMD)

## run: run with config.yaml (requires config.yaml to exist)
run: build
	./bin/$(BIN) -config config.yaml

## vet: run go vet
vet:
	go vet ./...

## test: run all tests
test:
	go test -race ./...

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
