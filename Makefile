BINARY  := fadwix-adminbot
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build run check test cover lint tidy clean docker

help: ## List the available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into bin/
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/bot

run: ## Run locally
	go run ./cmd/bot

check: ## Validate the configuration and access without starting the poll loop
	go run ./cmd/bot -check

test: ## Run the tests with the race detector
	go test -race ./...

cover: ## Report test coverage
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint: ## go vet plus a formatting check
	go vet ./...
	@test -z "$$(gofmt -l . )" || (echo "not gofmt-ed:"; gofmt -l .; exit 1)

tidy: ## Tidy go.mod
	go mod tidy

docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) .

clean: ## Remove build artefacts
	rm -rf bin coverage.out
