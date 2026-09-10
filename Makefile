VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BIN     := bin/server

.PHONY: help run build test cover race lint docker deploy preview clean

help: ## List targets
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

run: ## Run locally on :8080
	go run ./cmd/server

build: ## Build the binary into bin/
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.buildVersion=$(VERSION)" -o $(BIN) ./cmd/server

test: ## Run the tests
	go test ./...

race: ## Run the tests under the race detector
	go test -race ./...

cover: ## Coverage report in cover.html
	go test -coverprofile=cover.out ./...
	go tool cover -html=cover.out -o cover.html
	@echo "report written to cover.html"

lint: ## gofmt check and go vet
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "files above are unformatted"; exit 1)
	go vet ./...

docker: ## Build the self-hosting image
	docker build --build-arg VERSION=$(VERSION) -t portfolio-backend:$(VERSION) .

deploy: ## Ship to Vercel
	vercel deploy --prod

preview: ## Save preview.svg from the local server
	curl -fsS "http://localhost:8080/terminal.svg" -o preview.svg
	@echo "preview.svg written — open it in a browser to see the animation"

clean: ## Remove build artefacts
	rm -rf bin cover.out cover.html preview.svg
