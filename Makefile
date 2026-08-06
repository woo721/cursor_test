.PHONY: fmt vet test test-race run build

fmt:
	gofmt -w cmd internal tests

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

run:
	API_KEY=$${API_KEY:-local-development-key} DATA_MODE=$${DATA_MODE:-memory} go run ./cmd/server

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/query-api ./cmd/server
