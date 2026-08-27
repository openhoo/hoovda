.PHONY: all build test test-race generate check-generated conformance clean

all: test build

build:
	go build -trimpath -ldflags "-s -w" -o bin/hoovda ./cmd/hoovda

test:
	go test ./...

test-race:
	go test -race ./...

generate:
	go generate ./internal/atspi

check-generated: generate
	git diff --exit-code -- internal/atspi/zz_generated_interfaces.go

conformance: build
	./bin/hoovda conformance --manifest oracle/corpus/manifest.json

clean:
	rm -rf bin coverage dist oracle/out
