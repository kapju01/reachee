GO ?= go

.PHONY: build test cover run fmt vet tidy clean

build:
	$(GO) build ./...

test:
	$(GO) test ./...

cover:
	$(GO) test -coverprofile=coverage.out ./internal/...
	$(GO) tool cover -func=coverage.out | tail -1

run:
	$(GO) run ./cmd/reachee

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -f coverage.out
