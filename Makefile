GO ?= go
LAMBDAS := raise attempt reply

.PHONY: build lambdas test cover run fmt vet tidy clean FORCE

build: lambdas
	$(GO) build ./...

lambdas: $(LAMBDAS:%=build/%.zip)

# The zip's hash is the Lambda's source_code_hash, so the archive must be
# byte-identical across rebuilds of the same source: no VCS stamp, no paths,
# a fixed mtime, and no extra file attributes. Otherwise every plan shows a
# code change that is not one.
build/%.zip: FORCE
	mkdir -p build/$*
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -buildvcs=false \
		-ldflags="-s -w" -tags lambda.norpc -o build/$*/bootstrap ./cmd/$*
	touch -t 202001010000 build/$*/bootstrap
	rm -f $@
	cd build/$* && zip -X -q ../$*.zip bootstrap

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
	rm -rf build
